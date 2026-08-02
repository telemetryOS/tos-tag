package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/core/activity"
	"github.com/telemetryos/tos-tag/core/audit"
	"github.com/telemetryos/tos-tag/core/classifier"
	"github.com/telemetryos/tos-tag/core/config"
	"github.com/telemetryos/tos-tag/core/deliveries"
	"github.com/telemetryos/tos-tag/core/jobs"
	"github.com/telemetryos/tos-tag/core/keystore"
	agentmemory "github.com/telemetryos/tos-tag/core/memory"
	"github.com/telemetryos/tos-tag/core/orgconfig"
	"github.com/telemetryos/tos-tag/core/slack"
	"github.com/telemetryos/tos-tag/core/triggers"
	"github.com/telemetryos/tos-tag/models"
	"github.com/telemetryos/tos-tag/types"
)

func newTestServer(t *testing.T, authenticated bool) (*Server, *slack.StubIngress) {
	t.Helper()
	cfg := config.DefaultConfiguration
	cfg.Auth.Enabled = authenticated
	if authenticated {
		cfg.Auth.AdminToken = "admin-test-token"
	}
	ingress := slack.NewStubIngress(4)
	if err := ingress.Start(context.Background(), func(_ context.Context, _ types.SlackEnvelope) (slack.AcceptResult, error) {
		return slack.AcceptResult{}, nil
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ingress.Stop(context.Background()) })
	auditLog, err := audit.NewMemoryAppender([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	srv, err := New(Dependencies{
		Config: &cfg, Ingress: ingress, Transport: slack.NewStubDelivery(), Jobs: jobs.NewMemoryQueue(nil),
		Deliveries: deliveries.NewMemoryQueue(nil), Decisions: classifier.NewMemoryDecisionStore(), Version: "test", Audit: auditLog,
		Triggers: triggers.NewStore(nil),
	})
	if err != nil {
		t.Fatal(err)
	}
	return srv, ingress
}

func TestKeystoreMutationNeverReturnsSecretValue(t *testing.T) {
	srv, _ := newTestServer(t, false)
	store, err := keystore.New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	srv.deps.Secrets = store
	body := []byte(`{"organization_id":"org","name":"LINEAR_API_KEY","purpose":"linear helper","value":"never-render-this-secret"}`)
	req := httptest.NewRequest(http.MethodPut, "/admin/api/keystore", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-TOS-TAG-CSRF", srv.csrf)
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusCreated || strings.Contains(response.Body.String(), "never-render-this-secret") {
		t.Fatalf("secret response status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/api/keystore?organization_id=org", nil))
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "never-render-this-secret") {
		t.Fatalf("secret list status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestTenantListsRequireOrganizationAndRedactExecutionPayloads(t *testing.T) {
	srv, _ := newTestServer(t, false)
	_, _, err := srv.deps.Jobs.Enqueue(context.Background(), jobs.Spec{OrganizationID: "org-a", WorkspaceID: "team", ChannelID: "channel", RootThreadTS: "1.0", SessionID: "session", Generation: 1, IdempotencyKey: "job-a", Kind: "agent", Input: "private prompt text", MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = srv.deps.Jobs.Enqueue(context.Background(), jobs.Spec{OrganizationID: "org-b", WorkspaceID: "team", ChannelID: "channel", RootThreadTS: "2.0", SessionID: "session", Generation: 1, IdempotencyKey: "job-b", Kind: "agent", Input: "other tenant text", MaxAttempts: 2})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/api/jobs", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("missing organization status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/api/jobs?organization_id=org-a", nil))
	body := response.Body.String()
	if response.Code != http.StatusOK || strings.Contains(body, "private prompt text") || strings.Contains(body, "other tenant text") || strings.Contains(body, "lease") || strings.Contains(body, "job-b") {
		t.Fatalf("tenant job list status=%d body=%s", response.Code, body)
	}
}

func TestOrganizationListSupportsHeaderSelector(t *testing.T) {
	srv, _ := newTestServer(t, false)
	organizations := orgconfig.NewMemory()
	_, _ = organizations.PutOrganization(context.Background(), models.Organization{PublicID: "org-b", Name: "Beta", EnrollmentMode: "allowlist"})
	_, _ = organizations.PutOrganization(context.Background(), models.Organization{PublicID: "org-a", Name: "Alpha", EnrollmentMode: "all_observable_channels"})
	srv.deps.Organizations = organizations
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/api/organizations", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var listed []map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0]["id"] != "org-a" || listed[0]["name"] != "Alpha" {
		t.Fatalf("listed=%#v", listed)
	}
	if _, exists := listed[0]["created_at"]; exists {
		t.Fatalf("organization selector response exposed internal metadata: %#v", listed[0])
	}
}

func TestManagementRequiresBearerWhenEnabled(t *testing.T) {
	srv, _ := newTestServer(t, true)
	req := httptest.NewRequest(http.MethodGet, "/admin/api/status", nil)
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/admin/api/status", nil)
	req.Header.Set("Authorization", "Bearer admin-test-token")
	response = httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("authenticated status = %d: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "admin-test-token") {
		t.Fatal("status leaked the admin token")
	}
}

func TestStatusSupportsLiveModeWithoutStubAdapters(t *testing.T) {
	cfg := config.DefaultConfiguration
	cfg.Slack.Mode = "socket_mode"
	cfg.Slack.LiveEnabled = true
	srv, err := New(Dependencies{
		Config: &cfg, Jobs: jobs.NewMemoryQueue(nil), Deliveries: deliveries.NewMemoryQueue(nil),
		Decisions: classifier.NewMemoryDecisionStore(), Version: "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/.status", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("live status = %d: %s", response.Code, response.Body.String())
	}
}

func TestMutationRequiresCSRFAndAcknowledgesAcceptedEnvelope(t *testing.T) {
	srv, ingress := newTestServer(t, false)
	now := time.Now().UTC()
	envelope := types.SlackEnvelope{OrganizationID: "org", EnvelopeID: "env", EventID: "event", TeamID: "team", ChannelID: "channel", MessageTS: "1.1", UserID: "user", Kind: types.SlackEventMessage, Text: "hello", EventTime: now, ReceivedAt: now}
	body, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/admin/api/stub/envelopes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusForbidden {
		t.Fatalf("missing CSRF status = %d", response.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/admin/api/stub/envelopes", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-TOS-TAG-CSRF", srv.csrf)
	response = httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusAccepted {
		t.Fatalf("accepted status = %d: %s", response.Code, response.Body.String())
	}
	if len(ingress.Acks()) != 1 {
		t.Fatalf("acks = %d", len(ingress.Acks()))
	}
}

func TestDotRoutesAreRedactedAndManagementPageIsEmbedded(t *testing.T) {
	srv, _ := newTestServer(t, false)
	for _, path := range []string{"/.health", "/.version", "/.status", "/admin/"} {
		response := httptest.NewRecorder()
		srv.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, response.Code)
		}
	}
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/", nil))
	if !strings.Contains(response.Body.String(), "Real-time control-plane activity") || !strings.Contains(response.Body.String(), "Developer and diagnostic tools") {
		t.Fatal("management page missing primary live activity or progressive diagnostics")
	}
	for _, want := range []string{"--accent: #f8b334", "--page: #1b2632", `class="topstripe"`, `class="sidebar"`, `telemetry<strong>OS</strong>`} {
		if !strings.Contains(response.Body.String(), want) {
			t.Fatalf("management page missing Agent Wiki visual-system marker %q", want)
		}
	}
	if !strings.Contains(response.Body.String(), `id="organizationSelector"`) {
		t.Fatal("management page missing global organization selector")
	}
	if !strings.Contains(response.Body.String(), `new Set(['organization_id', 'workspace_id', 'team_id'])`) {
		t.Fatal("management tables do not hide tenant identifier columns")
	}
}

func TestActivityAPIAndSSEAreOrganizationScoped(t *testing.T) {
	srv, _ := newTestServer(t, false)
	srv.activity.Publish(activity.Record{OrganizationID: "org-a", Category: "classifier", Kind: "classification.completed", Title: "Classifier decision", Message: "public question", Summary: "Reply in channel"})
	srv.activity.Publish(activity.Record{OrganizationID: "org-b", Category: "classifier", Kind: "classification.completed", Title: "Other tenant decision", Message: "other tenant message"})

	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/api/activity", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("missing organization status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/api/activity?organization_id=org-a", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "public question") || strings.Contains(response.Body.String(), "other tenant message") {
		t.Fatalf("activity status=%d body=%s", response.Code, response.Body.String())
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/admin/events?organization_id=org-a", nil).WithContext(ctx)
	response = httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, request)
	body := response.Body.String()
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "text/event-stream" || !strings.Contains(body, "event: activity") || !strings.Contains(body, "public question") || strings.Contains(body, "other tenant message") {
		t.Fatalf("SSE status=%d headers=%v body=%s", response.Code, response.Header(), body)
	}
}

func TestDedicatedManagementPages(t *testing.T) {
	srv, _ := newTestServer(t, false)
	pages := []string{"channels", "observations", "decisions", "context", "jobs", "routes", "marketplaces", "tools", "approvals", "routines", "triggers", "notes", "memory", "directives", "keystore", "retention", "audit", "usage"}
	for _, page := range pages {
		response := httptest.NewRecorder()
		srv.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/"+page, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d: %s", page, response.Code, response.Body.String())
		}
		expectedHeading := `<h1 class="route-title">` + managementPageCopy[page].Title + `</h1>`
		if !strings.Contains(response.Body.String(), expectedHeading) {
			t.Fatalf("%s did not render its dedicated page", page)
		}
		if strings.Contains(response.Body.String(), `const page = "\"`) || strings.Contains(response.Body.String(), `const endpoint = "\"`) {
			t.Fatalf("%s rendered double-quoted JavaScript configuration", page)
		}
		if page != "keystore" && (strings.Contains(response.Body.String(), `id="queryForm"`) || strings.Contains(response.Body.String(), `id="mutationForm"`)) {
			t.Fatalf("%s rendered redundant organization or mutation forms", page)
		}
	}

	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/not-a-page", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("unknown page status = %d", response.Code)
	}
}

func TestMemoryManagementControlsCorrectPinAndForget(t *testing.T) {
	srv, _ := newTestServer(t, false)
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	store := agentmemory.NewMemoryStore(func() time.Time { return now })
	record, _, err := store.PutGenerated(context.Background(), agentmemory.Record{OrganizationID: "org", ChannelID: "channel", Scope: agentmemory.ScopeChannel, ScopeKey: "org/channel", Text: "generated memory", SourceHash: "hash", Status: agentmemory.StatusActive, ExpiresAt: now.Add(time.Hour), NaturalExpiresAt: now.Add(time.Hour)})
	if err != nil {
		t.Fatal(err)
	}
	srv.deps.Memory = store
	mutate := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-TOS-TAG-CSRF", srv.csrf)
		response := httptest.NewRecorder()
		srv.Handler().ServeHTTP(response, request)
		return response
	}
	response := mutate("/admin/api/memory/correct", `{"organization_id":"org","record_id":"`+record.ID+`","text":"operator correction","actor_id":"admin"}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "operator correction") || !strings.Contains(response.Body.String(), `"pinned":true`) {
		t.Fatalf("correct status=%d body=%s", response.Code, response.Body.String())
	}
	response = mutate("/admin/api/memory/forget", `{"organization_id":"org","record_id":"`+record.ID+`","actor_id":"admin"}`)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "operator correction") || !strings.Contains(response.Body.String(), `"status":"forgotten"`) {
		t.Fatalf("forget status=%d body=%s", response.Code, response.Body.String())
	}
}

func TestTriggerSubscriptionAPIIsTenantScopedAndValidated(t *testing.T) {
	srv, _ := newTestServer(t, false)
	body := []byte(`{"id":"heartbeat","organization_id":"org-a","workspace_id":"team","channel_id":"channel","session_id":"session","generation":1,"owner_id":"human-admin","kind":"heartbeat","instruction":"Check for work that needs Tag.","interval_seconds":300,"next_run":"2030-01-01T00:00:00Z","classifier_gate":true,"min_confidence":0.8,"enabled":true}`)
	req := httptest.NewRequest(http.MethodPut, "/admin/api/trigger-subscriptions", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-TOS-TAG-CSRF", srv.csrf)
	response := httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("put status=%d body=%s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/api/trigger-subscriptions", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("missing organization status=%d body=%s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/admin/api/trigger-subscriptions?organization_id=org-a", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"id":"heartbeat"`) {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}

	invalid := bytes.Replace(body, []byte(`"classifier_gate":true`), []byte(`"classifier_gate":false`), 1)
	req = httptest.NewRequest(http.MethodPut, "/admin/api/trigger-subscriptions", bytes.NewReader(invalid))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-TOS-TAG-CSRF", srv.csrf)
	response = httptest.NewRecorder()
	srv.Handler().ServeHTTP(response, req)
	if response.Code != http.StatusUnprocessableEntity {
		t.Fatalf("ungated heartbeat status=%d body=%s", response.Code, response.Body.String())
	}
}
