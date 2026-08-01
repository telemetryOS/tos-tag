// Package server exposes the redacted management plane. Stub-only controls are
// available only when the deterministic Slack adapter is configured.
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/RobertWHurst/blackbox"

	"github.com/telemetryos/tos-tag/core/approvals"
	"github.com/telemetryos/tos-tag/core/audit"
	"github.com/telemetryos/tos-tag/core/channelconfig"
	"github.com/telemetryos/tos-tag/core/classifier"
	"github.com/telemetryos/tos-tag/core/config"
	"github.com/telemetryos/tos-tag/core/deliveries"
	"github.com/telemetryos/tos-tag/core/intelligence"
	"github.com/telemetryos/tos-tag/core/jobs"
	"github.com/telemetryos/tos-tag/core/keystore"
	"github.com/telemetryos/tos-tag/core/management"
	"github.com/telemetryos/tos-tag/core/marketplace"
	"github.com/telemetryos/tos-tag/core/modelrouter"
	"github.com/telemetryos/tos-tag/core/orgconfig"
	"github.com/telemetryos/tos-tag/core/retention"
	"github.com/telemetryos/tos-tag/core/routines"
	"github.com/telemetryos/tos-tag/core/tools"
	"github.com/telemetryos/tos-tag/core/triggers"
	"github.com/telemetryos/tos-tag/models"
	"github.com/telemetryos/tos-tag/types"
)

//go:embed templates/index.html
var indexHTML string

type Pinger interface{ Ping(context.Context) error }

type StubIngress interface {
	Inject(context.Context, types.SlackEnvelope) (types.SlackAck, error)
	Acks() []types.SlackAck
}

type StubDelivery interface {
	Requests() []types.SlackDeliveryRequest
}

type Dependencies struct {
	Config              *config.Config
	Logger              *blackbox.Logger
	Health              Pinger
	Ingress             StubIngress
	Transport           StubDelivery
	Jobs                jobs.Queue
	Deliveries          deliveries.Queue
	Decisions           classifier.DecisionStore
	Version             string
	Routes              *modelrouter.Registry
	Organizations       orgconfig.Store
	Retention           *retention.Janitor
	Records             management.Reader
	ChannelConfig       channelconfig.Repository
	Marketplaces        *marketplace.Registry
	ToolMarketplaces    *tools.Registry
	Intelligence        *intelligence.Mongo
	Secrets             keystore.Repository
	Audit               audit.Appender
	Approvals           approvals.Repository
	ApprovalCoordinator interface {
		HandleSlackDecision(context.Context, approvals.SlackDecision) error
	}
	Routines routines.Repository
	Triggers triggers.Repository
}

type Server struct {
	deps       Dependencies
	template   *template.Template
	csrf       string
	handler    http.Handler
	httpServer *http.Server
	listener   net.Listener
	events     *eventHub
}

func New(deps Dependencies) (*Server, error) {
	if deps.Config == nil || deps.Jobs == nil || deps.Deliveries == nil || deps.Decisions == nil {
		return nil, fmt.Errorf("server dependencies are incomplete")
	}
	tmpl, err := template.New("index").Parse(indexHTML)
	if err != nil {
		return nil, fmt.Errorf("parse management template: %w", err)
	}
	csrf, err := randomToken()
	if err != nil {
		return nil, fmt.Errorf("create CSRF token: %w", err)
	}
	s := &Server{deps: deps, template: tmpl, csrf: csrf, events: newEventHub()}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.health", s.health)
	mux.HandleFunc("GET /.version", s.version)
	mux.HandleFunc("GET /.status", s.status)
	mux.HandleFunc("GET /admin", s.renderIndex)
	mux.HandleFunc("GET /admin/", s.renderIndex)
	mux.HandleFunc("GET /admin/api/csrf", s.csrfToken)
	mux.HandleFunc("GET /admin/api/status", s.status)
	mux.HandleFunc("GET /admin/api/jobs", s.listJobs)
	mux.HandleFunc("GET /admin/api/deliveries", s.listDeliveries)
	mux.HandleFunc("GET /admin/api/decisions", s.listDecisions)
	mux.HandleFunc("GET /admin/api/routes", s.listRoutes)
	mux.HandleFunc("POST /admin/api/routes/preview", s.previewRoute)
	mux.HandleFunc("PUT /admin/api/routes/profiles", s.putProfile)
	mux.HandleFunc("PUT /admin/api/routes/rules", s.putRule)
	mux.HandleFunc("GET /admin/api/channels", s.listChannels)
	mux.HandleFunc("PUT /admin/api/organizations", s.putOrganization)
	mux.HandleFunc("PUT /admin/api/workspaces", s.putWorkspace)
	mux.HandleFunc("PUT /admin/api/channels", s.putChannel)
	mux.HandleFunc("GET /admin/api/retention", s.retentionStatus)
	mux.HandleFunc("GET /admin/api/observations", s.listRecords("observations"))
	mux.HandleFunc("GET /admin/api/context", s.listRecords("context"))
	mux.HandleFunc("GET /admin/api/audit", s.listRecords("audit"))
	mux.HandleFunc("GET /admin/api/usage", s.listRecords("usage"))
	mux.HandleFunc("GET /admin/api/facts", s.listRecords("facts"))
	mux.HandleFunc("GET /admin/api/marketplaces", s.listMarketplaces)
	mux.HandleFunc("GET /admin/api/tool-marketplaces", s.listToolMarketplaces)
	mux.HandleFunc("GET /admin/api/projector", s.projectorStatus)
	mux.HandleFunc("GET /admin/api/directives", s.listDirectives)
	mux.HandleFunc("POST /admin/api/directives", s.draftDirective)
	mux.HandleFunc("POST /admin/api/directives/activate", s.activateDirective)
	mux.HandleFunc("GET /admin/api/notes", s.listNotes)
	mux.HandleFunc("POST /admin/api/notes", s.proposeNote)
	mux.HandleFunc("POST /admin/api/notes/review", s.reviewNote)
	mux.HandleFunc("GET /admin/api/keystore", s.listSecrets)
	mux.HandleFunc("PUT /admin/api/keystore", s.putSecret)
	mux.HandleFunc("GET /admin/api/approvals", s.listApprovals)
	mux.HandleFunc("POST /admin/api/approvals/approve", s.approveToolAction)
	mux.HandleFunc("GET /admin/api/routines", s.listRoutines)
	mux.HandleFunc("PUT /admin/api/routines", s.putRoutine)
	mux.HandleFunc("GET /admin/api/trigger-subscriptions", s.listTriggerSubscriptions)
	mux.HandleFunc("PUT /admin/api/trigger-subscriptions", s.putTriggerSubscription)
	mux.HandleFunc("GET /admin/events", s.eventStream)
	mux.HandleFunc("POST /admin/api/stub/envelopes", s.injectEnvelope)
	s.handler = s.securityHeaders(s.authenticate(mux))
	s.httpServer = &http.Server{Addr: deps.Config.HTTP.Addr, Handler: s.handler, ReadHeaderTimeout: deps.Config.HTTP.ReadHeaderTimeout}
	return s, nil
}

func (s *Server) Handler() http.Handler { return s.handler }

func (s *Server) Listen() error {
	listener, err := net.Listen("tcp", s.deps.Config.HTTP.Addr)
	if err != nil {
		return fmt.Errorf("bind %s: %w", s.deps.Config.HTTP.Addr, err)
	}
	s.listener = listener
	go func() {
		if err := s.httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) && s.deps.Logger != nil {
			s.deps.Logger.Error("management HTTP server failed", err)
		}
	}()
	return nil
}

func (s *Server) Shutdown(ctx context.Context) error { return s.httpServer.Shutdown(ctx) }

func (s *Server) Addr() string {
	if s.listener == nil {
		return ""
	}
	return s.listener.Addr().String()
}

func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/admin") || !s.deps.Config.Auth.Enabled {
			next.ServeHTTP(w, r)
			return
		}
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		want := s.deps.Config.Auth.AdminToken
		if len(got) != len(want) || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="tos-tag"`)
			writeError(w, http.StatusUnauthorized, "authentication_required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	if s.deps.Health != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.deps.Health.Ping(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "mongo": "unavailable"})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"service": "tos-tag", "version": s.deps.Version})
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	jobList, jobErr := s.deps.Jobs.List(r.Context())
	deliveryList, deliveryErr := s.deps.Deliveries.List(r.Context())
	decisionList, decisionErr := s.deps.Decisions.List(r.Context())
	if jobErr != nil || deliveryErr != nil || decisionErr != nil {
		writeError(w, http.StatusInternalServerError, "status_unavailable")
		return
	}
	acks, sends := 0, 0
	if s.deps.Ingress != nil {
		acks = len(s.deps.Ingress.Acks())
	}
	if s.deps.Transport != nil {
		sends = len(s.deps.Transport.Requests())
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"service": "tos-tag", "version": s.deps.Version, "configuration": s.deps.Config.RedactedStatus(),
		"counts":             map[string]int{"jobs": len(jobList), "deliveries": len(deliveryList), "decisions": len(decisionList), "acks": acks, "stub_sends": sends},
		"live_slack_enabled": s.deps.Config.Slack.LiveEnabled, "model_provider_enabled": s.deps.Config.Codex.Enabled,
	})
}

type pageData struct {
	Page     string
	Endpoint string
}

func (s *Server) renderIndex(w http.ResponseWriter, r *http.Request) {
	page := strings.Trim(strings.TrimPrefix(r.URL.Path, "/admin"), "/")
	if page == "" {
		page = "overview"
	}
	endpoints := map[string]string{
		"channels":     "/admin/api/channels",
		"observations": "/admin/api/observations",
		"decisions":    "/admin/api/decisions",
		"context":      "/admin/api/context",
		"jobs":         "/admin/api/jobs",
		"routes":       "/admin/api/routes",
		"marketplaces": "/admin/api/marketplaces",
		"tools":        "/admin/api/tool-marketplaces",
		"notes":        "/admin/api/notes",
		"directives":   "/admin/api/directives",
		"retention":    "/admin/api/retention",
		"audit":        "/admin/api/audit",
		"usage":        "/admin/api/usage",
		"keystore":     "/admin/api/keystore",
		"approvals":    "/admin/api/approvals",
		"routines":     "/admin/api/routines",
		"triggers":     "/admin/api/trigger-subscriptions",
	}
	endpoint := endpoints[page]
	if page != "overview" && endpoint == "" {
		writeError(w, http.StatusNotFound, "page_not_found")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.template.Execute(w, pageData{Page: page, Endpoint: endpoint}); err != nil && s.deps.Logger != nil {
		s.deps.Logger.Error("render management index", err)
	}
}

func (s *Server) csrfToken(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"token": s.csrf})
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := requiredOrganization(w, r)
	if !ok {
		return
	}
	value, err := s.deps.Jobs.ListOrganization(r.Context(), organizationID)
	if err != nil {
		writeList(w, nil, err)
		return
	}
	redacted := make([]map[string]any, len(value))
	for i, job := range value {
		redacted[i] = map[string]any{"id": job.ID, "organization_id": job.OrganizationID, "workspace_id": job.WorkspaceID, "channel_id": job.ChannelID, "root_thread_ts": job.RootThreadTS, "session_id": job.SessionID, "generation": job.Generation, "observation_id": job.ObservationID, "kind": job.Kind, "state": job.State, "attempt": job.Attempt, "max_attempts": job.MaxAttempts, "resolved_model": job.ResolvedModel, "route_trace": job.RouteTrace, "failure_reason": job.FailureReason, "created_at": job.CreatedAt, "updated_at": job.UpdatedAt, "version": job.Version}
	}
	writeJSON(w, http.StatusOK, redacted)
}

func (s *Server) listDeliveries(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := requiredOrganization(w, r)
	if !ok {
		return
	}
	value, err := s.deps.Deliveries.ListOrganization(r.Context(), organizationID)
	if err != nil {
		writeList(w, nil, err)
		return
	}
	redacted := make([]map[string]any, len(value))
	for i, delivery := range value {
		redacted[i] = map[string]any{"id": delivery.ID, "organization_id": delivery.OrganizationID, "job_id": delivery.JobID, "destination": delivery.Destination, "status": delivery.Status, "attempt": delivery.Attempt, "max_attempts": delivery.MaxAttempts, "retry_at": delivery.RetryAt, "slack_message_ts": delivery.SlackMessageTS, "failure_reason": delivery.FailureReason, "created_at": delivery.CreatedAt, "updated_at": delivery.UpdatedAt, "version": delivery.Version}
	}
	writeJSON(w, http.StatusOK, redacted)
}

func (s *Server) listDecisions(w http.ResponseWriter, r *http.Request) {
	organizationID, ok := requiredOrganization(w, r)
	if !ok {
		return
	}
	value, err := s.deps.Decisions.ListOrganization(r.Context(), organizationID)
	writeList(w, value, err)
}

func requiredOrganization(w http.ResponseWriter, r *http.Request) (string, bool) {
	organizationID := strings.TrimSpace(r.URL.Query().Get("organization_id"))
	if organizationID == "" {
		writeError(w, http.StatusBadRequest, "organization_id_required")
		return "", false
	}
	return organizationID, true
}

func (s *Server) injectEnvelope(w http.ResponseWriter, r *http.Request) {
	if s.deps.Ingress == nil {
		writeError(w, http.StatusNotFound, "stub_disabled")
		return
	}
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-TOS-TAG-CSRF")), []byte(s.csrf)) != 1 {
		writeError(w, http.StatusForbidden, "csrf_invalid")
		return
	}
	if mediaType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])); mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "application_json_required")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	var envelope types.SlackEnvelope
	if err := decoder.Decode(&envelope); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_envelope")
		return
	}
	ack, err := s.deps.Ingress.Inject(r.Context(), envelope)
	if err != nil {
		if s.deps.Logger != nil {
			s.deps.Logger.Error("stub envelope rejected", err)
		}
		writeError(w, http.StatusUnprocessableEntity, "envelope_rejected")
		return
	}
	writeJSON(w, http.StatusAccepted, ack)
	s.events.Publish("observations")
}

func (s *Server) listRoutes(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Routes == nil {
		writeError(w, http.StatusNotFound, "routes_disabled")
		return
	}
	writeJSON(w, http.StatusOK, s.deps.Routes.Snapshot())
}
func (s *Server) previewRoute(w http.ResponseWriter, r *http.Request) {
	if s.deps.Routes == nil {
		writeError(w, http.StatusNotFound, "routes_disabled")
		return
	}
	var input struct {
		Route       types.ModelRouteContext `json:"route"`
		Constraints modelrouter.Constraints `json:"constraints"`
	}
	if !decodeMutation(w, r, s.csrf, &input) {
		return
	}
	resolved, trace, err := s.deps.Routes.Resolve(r.Context(), input.Route, input.Constraints)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "no_eligible_route")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"resolved": resolved, "trace": trace, "policy": s.deps.Routes.Snapshot().PolicyRevision})
}
func (s *Server) putProfile(w http.ResponseWriter, r *http.Request) {
	if s.deps.Routes == nil {
		writeError(w, http.StatusNotFound, "routes_disabled")
		return
	}
	var profile types.ModelProfile
	if !decodeMutation(w, r, s.csrf, &profile) {
		return
	}
	if !s.auditMutation(w, r, r.URL.Query().Get("organization_id"), profile.ID, "model_profile.put", "admin", map[string]any{"provider_id": profile.ProviderID, "enabled": profile.Enabled}) {
		return
	}
	if err := s.deps.Routes.PutProfileContext(r.Context(), profile); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_profile")
		return
	}
	s.events.Publish("routes")
	writeJSON(w, http.StatusOK, profile)
}
func (s *Server) putRule(w http.ResponseWriter, r *http.Request) {
	if s.deps.Routes == nil {
		writeError(w, http.StatusNotFound, "routes_disabled")
		return
	}
	var rule modelrouter.Rule
	if !decodeMutation(w, r, s.csrf, &rule) {
		return
	}
	organizationID := rule.OrganizationID
	if organizationID == "" {
		organizationID = r.URL.Query().Get("organization_id")
	}
	if !s.auditMutation(w, r, organizationID, rule.ID, "model_rule.put", "admin", map[string]any{"profile_id": rule.ProfileID}) {
		return
	}
	if err := s.deps.Routes.PutRuleContext(r.Context(), rule); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_rule")
		return
	}
	s.events.Publish("routes")
	writeJSON(w, http.StatusOK, rule)
}
func (s *Server) listChannels(w http.ResponseWriter, r *http.Request) {
	if s.deps.Organizations == nil {
		writeError(w, http.StatusNotFound, "channels_disabled")
		return
	}
	organizationID, ok := requiredOrganization(w, r)
	if !ok {
		return
	}
	values, err := s.deps.Organizations.ListChannels(r.Context(), organizationID)
	writeList(w, values, err)
}
func (s *Server) putOrganization(w http.ResponseWriter, r *http.Request) {
	if s.deps.Organizations == nil {
		writeError(w, http.StatusNotFound, "organizations_disabled")
		return
	}
	var input struct {
		PublicID            string `json:"public_id"`
		Name                string `json:"name"`
		EnrollmentMode      string `json:"enrollment_mode"`
		KillSwitch          bool   `json:"kill_switch"`
		DefaultModelProfile string `json:"default_model_profile"`
	}
	if !decodeMutation(w, r, s.csrf, &input) {
		return
	}
	value := models.Organization{PublicID: input.PublicID, Name: input.Name, EnrollmentMode: input.EnrollmentMode, KillSwitch: input.KillSwitch, DefaultModelProfile: input.DefaultModelProfile}
	if !s.auditMutation(w, r, value.PublicID, value.PublicID, "organization.put", "admin", map[string]any{"kill_switch": value.KillSwitch, "enrollment_mode": value.EnrollmentMode}) {
		return
	}
	saved, err := s.deps.Organizations.PutOrganization(r.Context(), value)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_organization")
		return
	}
	s.events.Publish("organizations")
	writeJSON(w, http.StatusOK, saved)
}
func (s *Server) putWorkspace(w http.ResponseWriter, r *http.Request) {
	if s.deps.Organizations == nil {
		writeError(w, http.StatusNotFound, "workspaces_disabled")
		return
	}
	var input struct {
		OrganizationID string `json:"organization_id"`
		TeamID         string `json:"team_id"`
		Name           string `json:"name"`
		Enabled        bool   `json:"enabled"`
	}
	if !decodeMutation(w, r, s.csrf, &input) {
		return
	}
	value := models.Workspace{OrganizationID: input.OrganizationID, TeamID: input.TeamID, Name: input.Name, Enabled: input.Enabled}
	if !s.auditMutation(w, r, value.OrganizationID, value.TeamID, "workspace.put", "admin", map[string]any{"enabled": value.Enabled}) {
		return
	}
	saved, err := s.deps.Organizations.PutWorkspace(r.Context(), value)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_workspace")
		return
	}
	s.events.Publish("workspaces")
	writeJSON(w, http.StatusOK, saved)
}
func (s *Server) putChannel(w http.ResponseWriter, r *http.Request) {
	if s.deps.Organizations == nil {
		writeError(w, http.StatusNotFound, "channels_disabled")
		return
	}
	var policy orgconfig.ChannelPolicy
	if !decodeMutation(w, r, s.csrf, &policy) {
		return
	}
	if !s.auditMutation(w, r, policy.OrganizationID, policy.ChannelID, "channel_policy.put", "admin", map[string]any{"team_id": policy.TeamID, "enrolled": policy.Enrolled, "restricted": policy.Restricted, "kill_switch": policy.KillSwitch}) {
		return
	}
	saved, err := s.deps.Organizations.PutChannel(r.Context(), policy)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_channel_policy")
		return
	}
	s.events.Publish("channels")
	writeJSON(w, http.StatusOK, saved)
}
func (s *Server) retentionStatus(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Retention == nil {
		writeError(w, http.StatusNotFound, "retention_disabled")
		return
	}
	writeJSON(w, http.StatusOK, s.deps.Retention.Status())
}

func (s *Server) listRecords(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s.deps.Records == nil {
			writeError(w, http.StatusNotFound, "records_disabled")
			return
		}
		values, err := s.deps.Records.List(r.Context(), kind, r.URL.Query().Get("organization_id"), 100)
		writeList(w, values, err)
	}
}
func (s *Server) listMarketplaces(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Marketplaces == nil {
		writeJSON(w, http.StatusOK, []marketplace.SkillSnapshot{})
		return
	}
	writeJSON(w, http.StatusOK, s.deps.Marketplaces.List())
}
func (s *Server) listToolMarketplaces(w http.ResponseWriter, _ *http.Request) {
	if s.deps.ToolMarketplaces == nil {
		writeJSON(w, http.StatusOK, []tools.Snapshot{})
		return
	}
	writeJSON(w, http.StatusOK, s.deps.ToolMarketplaces.List())
}
func (s *Server) projectorStatus(w http.ResponseWriter, r *http.Request) {
	if s.deps.Intelligence == nil {
		writeError(w, http.StatusNotFound, "projector_disabled")
		return
	}
	status, err := s.deps.Intelligence.Status(r.Context(), r.URL.Query().Get("organization_id"))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "projector_unavailable")
		return
	}
	writeJSON(w, http.StatusOK, status)
}
func (s *Server) listDirectives(w http.ResponseWriter, r *http.Request) {
	if s.deps.ChannelConfig == nil {
		writeError(w, http.StatusNotFound, "directives_disabled")
		return
	}
	values, err := s.deps.ChannelConfig.ListDirectives(r.Context(), r.URL.Query().Get("organization_id"), r.URL.Query().Get("channel_id"))
	writeList(w, values, err)
}
func (s *Server) draftDirective(w http.ResponseWriter, r *http.Request) {
	if s.deps.ChannelConfig == nil {
		writeError(w, http.StatusNotFound, "directives_disabled")
		return
	}
	var input struct {
		OrganizationID string `json:"organization_id"`
		ChannelID      string `json:"channel_id"`
		Prompt         string `json:"prompt"`
		ActorID        string `json:"actor_id"`
	}
	if !decodeMutation(w, r, s.csrf, &input) {
		return
	}
	if !s.auditMutation(w, r, input.OrganizationID, input.ChannelID, "directive.draft", input.ActorID, map[string]any{"channel_id": input.ChannelID}) {
		return
	}
	value, err := s.deps.ChannelConfig.DraftDirective(r.Context(), input.OrganizationID, input.ChannelID, input.Prompt, input.ActorID)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_directive")
		return
	}
	s.events.Publish("directives")
	writeJSON(w, http.StatusCreated, value)
}
func (s *Server) activateDirective(w http.ResponseWriter, r *http.Request) {
	if s.deps.ChannelConfig == nil {
		writeError(w, http.StatusNotFound, "directives_disabled")
		return
	}
	var input struct {
		OrganizationID string `json:"organization_id"`
		ChannelID      string `json:"channel_id"`
		RevisionID     string `json:"revision_id"`
	}
	if !decodeMutation(w, r, s.csrf, &input) {
		return
	}
	if !s.auditMutation(w, r, input.OrganizationID, input.RevisionID, "directive.activate", "admin", map[string]any{"channel_id": input.ChannelID}) {
		return
	}
	value, err := s.deps.ChannelConfig.ActivateDirective(r.Context(), input.OrganizationID, input.ChannelID, input.RevisionID)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_directive_revision")
		return
	}
	s.events.Publish("directives")
	writeJSON(w, http.StatusOK, value)
}
func (s *Server) listNotes(w http.ResponseWriter, r *http.Request) {
	if s.deps.ChannelConfig == nil {
		writeError(w, http.StatusNotFound, "notes_disabled")
		return
	}
	values, err := s.deps.ChannelConfig.ListNotes(r.Context(), r.URL.Query().Get("organization_id"), r.URL.Query().Get("channel_id"))
	writeList(w, values, err)
}
func (s *Server) proposeNote(w http.ResponseWriter, r *http.Request) {
	if s.deps.ChannelConfig == nil {
		writeError(w, http.StatusNotFound, "notes_disabled")
		return
	}
	var input struct {
		OrganizationID string   `json:"organization_id"`
		ChannelID      string   `json:"channel_id"`
		Text           string   `json:"text"`
		SourceIDs      []string `json:"source_ids"`
		ActorID        string   `json:"actor_id"`
	}
	if !decodeMutation(w, r, s.csrf, &input) {
		return
	}
	if !s.auditMutation(w, r, input.OrganizationID, input.ChannelID, "note.propose", input.ActorID, map[string]any{"channel_id": input.ChannelID, "source_ids": input.SourceIDs}) {
		return
	}
	value, err := s.deps.ChannelConfig.ProposeNote(r.Context(), input.OrganizationID, input.ChannelID, input.Text, input.SourceIDs, input.ActorID)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_note")
		return
	}
	s.events.Publish("notes")
	writeJSON(w, http.StatusCreated, value)
}

func (s *Server) auditMutation(w http.ResponseWriter, r *http.Request, organizationID, resourceID, receiptType, actorID string, metadata map[string]any) bool {
	if s.deps.Audit == nil {
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable")
		return false
	}
	organizationID = strings.TrimSpace(organizationID)
	if organizationID == "" {
		writeError(w, http.StatusBadRequest, "organization_id_required")
		return false
	}
	_, err := s.deps.Audit.Append(r.Context(), audit.AppendRequest{OrganizationID: organizationID, Type: receiptType + ".requested", ActorID: actorID, ResourceID: resourceID, Metadata: metadata, RetentionEpoch: time.Now().UTC().Format("2006-01")})
	if err != nil {
		if s.deps.Logger != nil {
			s.deps.Logger.Error("append audit receipt", err)
		}
		writeError(w, http.StatusServiceUnavailable, "audit_unavailable")
		return false
	}
	if s.deps.Logger != nil {
		s.deps.Logger.WithCtx(blackbox.Ctx{
			"organization_id": organizationID, "resource_id": resourceID,
			"action_type": receiptType, "actor_id": actorID,
			"http_method": r.Method, "http_path": r.URL.Path,
		}).Info("management action authorized and audited")
	}
	return true
}
func (s *Server) reviewNote(w http.ResponseWriter, r *http.Request) {
	if s.deps.ChannelConfig == nil {
		writeError(w, http.StatusNotFound, "notes_disabled")
		return
	}
	var input struct {
		OrganizationID string `json:"organization_id"`
		ChannelID      string `json:"channel_id"`
		NoteID         string `json:"note_id"`
		ReviewerID     string `json:"reviewer_id"`
		Approve        bool   `json:"approve"`
	}
	if !decodeMutation(w, r, s.csrf, &input) {
		return
	}
	if !s.auditMutation(w, r, input.OrganizationID, input.NoteID, "note.review", input.ReviewerID, map[string]any{"channel_id": input.ChannelID, "approved": input.Approve}) {
		return
	}
	value, err := s.deps.ChannelConfig.ReviewNote(r.Context(), input.OrganizationID, input.ChannelID, input.NoteID, input.ReviewerID, input.Approve)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_note_review")
		return
	}
	s.events.Publish("notes")
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) listSecrets(w http.ResponseWriter, r *http.Request) {
	if s.deps.Secrets == nil {
		writeError(w, http.StatusNotFound, "keystore_disabled")
		return
	}
	values, err := s.deps.Secrets.List(r.Context(), r.URL.Query().Get("organization_id"))
	writeList(w, values, err)
}

func (s *Server) putSecret(w http.ResponseWriter, r *http.Request) {
	if s.deps.Secrets == nil {
		writeError(w, http.StatusNotFound, "keystore_disabled")
		return
	}
	var input struct {
		OrganizationID string `json:"organization_id"`
		Name           string `json:"name"`
		Purpose        string `json:"purpose"`
		Value          string `json:"value"`
	}
	if !decodeMutation(w, r, s.csrf, &input) {
		return
	}
	if !s.auditMutation(w, r, input.OrganizationID, input.Name, "keystore.put", "admin", map[string]any{"purpose": input.Purpose}) {
		return
	}
	reference, err := s.deps.Secrets.Put(r.Context(), input.OrganizationID, input.Name, input.Purpose, input.Value)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_secret")
		return
	}
	s.events.Publish("secrets")
	writeJSON(w, http.StatusCreated, reference)
}

func (s *Server) listApprovals(w http.ResponseWriter, r *http.Request) {
	if s.deps.Approvals == nil {
		writeError(w, http.StatusNotFound, "approvals_disabled")
		return
	}
	organizationID, ok := requiredOrganization(w, r)
	if !ok {
		return
	}
	values, err := s.deps.Approvals.List(r.Context(), organizationID)
	writeList(w, values, err)
}

func (s *Server) approveToolAction(w http.ResponseWriter, r *http.Request) {
	if s.deps.Approvals == nil {
		writeError(w, http.StatusNotFound, "approvals_disabled")
		return
	}
	var input struct {
		OrganizationID string `json:"organization_id"`
		ApprovalID     string `json:"approval_id"`
		ApproverID     string `json:"approver_id"`
	}
	if !decodeMutation(w, r, s.csrf, &input) {
		return
	}
	if !s.auditMutation(w, r, input.OrganizationID, input.ApprovalID, "tool_approval.approve", input.ApproverID, nil) {
		return
	}
	value, err := s.deps.Approvals.GetContext(r.Context(), input.OrganizationID, input.ApprovalID)
	if err == nil && s.deps.ApprovalCoordinator != nil && value.Action.WorkspaceID != "" && value.Action.ChannelID != "" && value.Action.JobID != "" {
		err = s.deps.ApprovalCoordinator.HandleSlackDecision(r.Context(), approvals.SlackDecision{OrganizationID: input.OrganizationID, WorkspaceID: value.Action.WorkspaceID, ChannelID: value.Action.ChannelID, UserID: input.ApproverID, ApprovalID: input.ApprovalID, Approve: true})
		if err == nil {
			value, err = s.deps.Approvals.GetContext(r.Context(), input.OrganizationID, input.ApprovalID)
		}
	} else if err == nil {
		value, err = s.deps.Approvals.ApproveContext(r.Context(), input.OrganizationID, input.ApprovalID, input.ApproverID)
	}
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "approval_not_approvable")
		return
	}
	s.events.Publish("approvals")
	writeJSON(w, http.StatusOK, value)
}

func (s *Server) listRoutines(w http.ResponseWriter, r *http.Request) {
	if s.deps.Routines == nil {
		writeError(w, http.StatusNotFound, "routines_disabled")
		return
	}
	organizationID, ok := requiredOrganization(w, r)
	if !ok {
		return
	}
	values, err := s.deps.Routines.List(r.Context(), organizationID)
	writeList(w, values, err)
}

func (s *Server) putRoutine(w http.ResponseWriter, r *http.Request) {
	if s.deps.Routines == nil {
		writeError(w, http.StatusNotFound, "routines_disabled")
		return
	}
	var input struct {
		ID              string          `json:"id"`
		OrganizationID  string          `json:"organization_id"`
		WorkspaceID     string          `json:"workspace_id"`
		ChannelID       string          `json:"channel_id"`
		RootThreadTS    string          `json:"root_thread_ts"`
		SessionID       types.SessionID `json:"session_id"`
		Generation      int64           `json:"generation"`
		OwnerID         string          `json:"owner_id"`
		Input           string          `json:"input"`
		IntervalSeconds int64           `json:"interval_seconds"`
		NextRun         time.Time       `json:"next_run"`
		Enabled         bool            `json:"enabled"`
	}
	if !decodeMutation(w, r, s.csrf, &input) {
		return
	}
	if !s.auditMutation(w, r, input.OrganizationID, input.ID, "routine.put", input.OwnerID, map[string]any{"channel_id": input.ChannelID, "enabled": input.Enabled}) {
		return
	}
	value := routines.Routine{ID: input.ID, OrganizationID: input.OrganizationID, WorkspaceID: input.WorkspaceID, ChannelID: input.ChannelID, RootThreadTS: input.RootThreadTS, SessionID: input.SessionID, Generation: input.Generation, OwnerID: input.OwnerID, Input: input.Input, Interval: time.Duration(input.IntervalSeconds) * time.Second, NextRun: input.NextRun, Enabled: input.Enabled}
	saved, err := s.deps.Routines.PutContext(r.Context(), value)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_routine")
		return
	}
	s.events.Publish("routines")
	writeJSON(w, http.StatusOK, saved)
}

func (s *Server) listTriggerSubscriptions(w http.ResponseWriter, r *http.Request) {
	if s.deps.Triggers == nil {
		writeError(w, http.StatusNotFound, "trigger_subscriptions_disabled")
		return
	}
	organizationID, ok := requiredOrganization(w, r)
	if !ok {
		return
	}
	values, err := s.deps.Triggers.List(r.Context(), organizationID)
	writeList(w, values, err)
}

func (s *Server) putTriggerSubscription(w http.ResponseWriter, r *http.Request) {
	if s.deps.Triggers == nil {
		writeError(w, http.StatusNotFound, "trigger_subscriptions_disabled")
		return
	}
	var input struct {
		ID              string          `json:"id"`
		OrganizationID  string          `json:"organization_id"`
		WorkspaceID     string          `json:"workspace_id"`
		ChannelID       string          `json:"channel_id"`
		RootThreadTS    string          `json:"root_thread_ts"`
		SessionID       types.SessionID `json:"session_id"`
		Generation      int64           `json:"generation"`
		OwnerID         string          `json:"owner_id"`
		Kind            triggers.Kind   `json:"kind"`
		Instruction     string          `json:"instruction"`
		IntervalSeconds int64           `json:"interval_seconds"`
		NextRun         time.Time       `json:"next_run"`
		ClassifierGate  bool            `json:"classifier_gate"`
		MinConfidence   float64         `json:"min_confidence"`
		Enabled         bool            `json:"enabled"`
	}
	if !decodeMutation(w, r, s.csrf, &input) {
		return
	}
	if !s.auditMutation(w, r, input.OrganizationID, input.ID, "trigger_subscription.put", input.OwnerID, map[string]any{"channel_id": input.ChannelID, "kind": string(input.Kind), "enabled": input.Enabled, "classifier_gate": input.ClassifierGate}) {
		return
	}
	value := triggers.Subscription{
		ID: input.ID, OrganizationID: input.OrganizationID, WorkspaceID: input.WorkspaceID,
		ChannelID: input.ChannelID, RootThreadTS: input.RootThreadTS, SessionID: input.SessionID,
		Generation: input.Generation, OwnerID: input.OwnerID, Kind: input.Kind,
		Instruction: input.Instruction, Interval: time.Duration(input.IntervalSeconds) * time.Second,
		NextRun: input.NextRun, ClassifierGate: input.ClassifierGate,
		MinConfidence: input.MinConfidence, Enabled: input.Enabled,
	}
	saved, err := s.deps.Triggers.PutContext(r.Context(), value)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_trigger_subscription")
		return
	}
	s.events.Publish("trigger_subscriptions")
	writeJSON(w, http.StatusOK, saved)
}

func decodeMutation(w http.ResponseWriter, r *http.Request, csrf string, destination any) bool {
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("X-TOS-TAG-CSRF")), []byte(csrf)) != 1 {
		writeError(w, http.StatusForbidden, "csrf_invalid")
		return false
	}
	if mediaType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0])); mediaType != "application/json" {
		writeError(w, http.StatusUnsupportedMediaType, "application_json_required")
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_json")
		return false
	}
	return true
}

func writeList(w http.ResponseWriter, value any, err error) {
	if err != nil {
		writeError(w, http.StatusInternalServerError, "query_failed")
		return
	}
	writeJSON(w, http.StatusOK, value)
}

func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func randomToken() (string, error) {
	buffer := make([]byte, 32)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}
