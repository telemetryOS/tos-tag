// Package orgconfig owns organization, Slack workspace, and channel policy.
package orgconfig

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/telemetryos/tos-tag/core/database"
	"github.com/telemetryos/tos-tag/models"
	"github.com/telemetryos/tos-tag/types"
)

var ErrNotFound = errors.New("organization scope not found")

type ChannelPolicy struct {
	OrganizationID                   string                   `json:"organization_id"`
	TeamID                           string                   `json:"team_id"`
	ChannelID                        string                   `json:"channel_id"`
	Name                             string                   `json:"name,omitempty"`
	Enrolled                         bool                     `json:"enrolled"`
	Restricted                       bool                     `json:"restricted"`
	ParticipationMode                types.ParticipationMode  `json:"participation_mode"`
	KillSwitch                       bool                     `json:"kill_switch"`
	Cooldown                         time.Duration            `json:"cooldown"`
	MaxResponsesPerHour              int                      `json:"max_responses_per_hour"`
	MaxConcurrentJobs                int                      `json:"max_concurrent_jobs"`
	DefaultModelProfile              string                   `json:"default_model_profile,omitempty"`
	ContextHistoryMode               types.ContextHistoryMode `json:"context_history_mode"`
	ApproverUserIDs                  []string                 `json:"approver_user_ids,omitempty"`
	TrustedIntegrationBotIDs         []string                 `json:"trusted_integration_bot_ids,omitempty"`
	BotIsMember                      bool                     `json:"bot_is_member"`
	BotMembershipKnown               bool                     `json:"bot_membership_known"`
	ParticipationManagedByMembership bool                     `json:"participation_managed_by_membership"`
	WorkspaceEnabled                 bool                     `json:"workspace_enabled"`
	MembershipRevision               string                   `json:"membership_revision"`
	MembershipRefreshedAt            time.Time                `json:"membership_refreshed_at"`
	Version                          int64                    `json:"version"`
}

type Resolver interface {
	Resolve(context.Context, string, string, string) (ChannelPolicy, error)
}

type Store interface {
	Resolver
	GetOrganization(context.Context, string) (models.Organization, error)
	ListOrganizations(context.Context) ([]models.Organization, error)
	GetWorkspace(context.Context, string, string) (models.Workspace, error)
	PutOrganization(context.Context, models.Organization) (models.Organization, error)
	PutWorkspace(context.Context, models.Workspace) (models.Workspace, error)
	PutChannel(context.Context, ChannelPolicy) (ChannelPolicy, error)
	UpsertContextChannel(context.Context, ChannelPolicy) (ChannelPolicy, error)
	ListChannels(context.Context, string) ([]ChannelPolicy, error)
}

func ValidateChannel(policy ChannelPolicy) error {
	if policy.OrganizationID == "" || policy.TeamID == "" || policy.ChannelID == "" || policy.MembershipRevision == "" || policy.MembershipRefreshedAt.IsZero() {
		return fmt.Errorf("channel scope and membership revision are required")
	}
	switch policy.ParticipationMode {
	case types.ModeObserve, types.ModeMention, types.ModeAssist, types.ModeProactive:
	default:
		return fmt.Errorf("invalid participation mode %q", policy.ParticipationMode)
	}
	switch policy.ContextHistoryMode {
	case "", types.ContextHistoryDurable, types.ContextHistorySessionOnly:
	default:
		return fmt.Errorf("invalid context history mode %q", policy.ContextHistoryMode)
	}
	if policy.Cooldown < 0 || policy.MaxResponsesPerHour <= 0 || policy.MaxConcurrentJobs <= 0 {
		return fmt.Errorf("invalid channel admission limits")
	}
	seenApprovers := make(map[string]struct{}, len(policy.ApproverUserIDs))
	for _, userID := range policy.ApproverUserIDs {
		if userID == "" {
			return fmt.Errorf("channel approver user IDs must be non-empty")
		}
		if _, duplicate := seenApprovers[userID]; duplicate {
			return fmt.Errorf("channel approver user IDs must be unique")
		}
		seenApprovers[userID] = struct{}{}
	}
	seenIntegrationBots := make(map[string]struct{}, len(policy.TrustedIntegrationBotIDs))
	for _, botID := range policy.TrustedIntegrationBotIDs {
		if !validSlackBotID(botID) {
			return fmt.Errorf("channel trusted integration bot IDs must be exact Slack bot IDs")
		}
		if _, duplicate := seenIntegrationBots[botID]; duplicate {
			return fmt.Errorf("channel trusted integration bot IDs must be unique")
		}
		seenIntegrationBots[botID] = struct{}{}
	}
	return nil
}

func validSlackBotID(value string) bool {
	if len(value) < 2 || value[0] != 'B' {
		return false
	}
	for _, character := range value[1:] {
		if (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

type Memory struct {
	mu            sync.RWMutex
	organizations map[string]models.Organization
	workspaces    map[string]models.Workspace
	channels      map[string]ChannelPolicy
}

func NewMemory() *Memory {
	return &Memory{organizations: make(map[string]models.Organization), workspaces: make(map[string]models.Workspace), channels: make(map[string]ChannelPolicy)}
}
func scopeKey(org, team, channel string) string { return org + "/" + team + "/" + channel }
func (s *Memory) Resolve(_ context.Context, org, team, channel string) (ChannelPolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.channels[scopeKey(org, team, channel)]
	if !ok {
		return ChannelPolicy{}, ErrNotFound
	}
	if organization, exists := s.organizations[org]; exists && organization.KillSwitch {
		value.KillSwitch = true
	}
	if workspace, exists := s.workspaces[org+"/"+team]; exists {
		value.WorkspaceEnabled = workspace.Enabled
		value.KillSwitch = value.KillSwitch || !workspace.Enabled
	} else {
		return ChannelPolicy{}, ErrNotFound
	}
	return value, nil
}
func (s *Memory) GetOrganization(_ context.Context, organizationID string) (models.Organization, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.organizations[organizationID]
	if !ok {
		return models.Organization{}, ErrNotFound
	}
	return value, nil
}
func (s *Memory) ListOrganizations(_ context.Context) ([]models.Organization, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]models.Organization, 0, len(s.organizations))
	for _, organization := range s.organizations {
		out = append(out, organization)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == out[j].Name {
			return out[i].PublicID < out[j].PublicID
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}
func (s *Memory) PutOrganization(_ context.Context, value models.Organization) (models.Organization, error) {
	if value.PublicID == "" {
		return models.Organization{}, ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.organizations[value.PublicID]; ok {
		value.Version = old.Version + 1
	} else {
		value.Version = 1
	}
	s.organizations[value.PublicID] = value
	return value, nil
}
func (s *Memory) PutWorkspace(_ context.Context, value models.Workspace) (models.Workspace, error) {
	if value.OrganizationID == "" || value.TeamID == "" {
		return models.Workspace{}, ErrNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := value.OrganizationID + "/" + value.TeamID
	if old, ok := s.workspaces[key]; ok {
		value.Version = old.Version + 1
	} else {
		value.Version = 1
	}
	s.workspaces[key] = value
	return value, nil
}
func (s *Memory) GetWorkspace(_ context.Context, organizationID, teamID string) (models.Workspace, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.workspaces[organizationID+"/"+teamID]
	if !ok {
		return models.Workspace{}, ErrNotFound
	}
	return value, nil
}
func (s *Memory) PutChannel(_ context.Context, value ChannelPolicy) (ChannelPolicy, error) {
	if err := ValidateChannel(value); err != nil {
		return ChannelPolicy{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	value.ContextHistoryMode = normalizedContextHistoryMode(value.ContextHistoryMode)
	key := scopeKey(value.OrganizationID, value.TeamID, value.ChannelID)
	if old, ok := s.channels[key]; ok {
		value.Version = old.Version + 1
	} else {
		value.Version = 1
	}
	s.channels[key] = value
	return value, nil
}

// UpsertContextChannel refreshes Slack-observed membership metadata. Operator
// policy remains intact unless the caller explicitly enables membership-managed
// participation, in which case Slack bot membership owns observe/assist mode.
func (s *Memory) UpsertContextChannel(_ context.Context, value ChannelPolicy) (ChannelPolicy, error) {
	if err := ValidateChannel(value); err != nil {
		return ChannelPolicy{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := scopeKey(value.OrganizationID, value.TeamID, value.ChannelID)
	if current, ok := s.channels[key]; ok {
		if value.Name != "" {
			current.Name = value.Name
		}
		current.Restricted = current.Restricted || value.Restricted
		current.MembershipRevision = value.MembershipRevision
		current.MembershipRefreshedAt = value.MembershipRefreshedAt
		if value.BotMembershipKnown {
			current.BotIsMember = value.BotIsMember
			current.BotMembershipKnown = true
		}
		if value.ParticipationManagedByMembership {
			current.ParticipationMode = value.ParticipationMode
			current.ParticipationManagedByMembership = true
		} else if value.BotMembershipKnown && current.ParticipationManagedByMembership {
			current.ParticipationMode = types.ModeObserve
			current.ParticipationManagedByMembership = false
		}
		current.Version++
		s.channels[key] = current
		return current, nil
	}
	value.Version = 1
	s.channels[key] = value
	return value, nil
}
func (s *Memory) ListChannels(_ context.Context, org string) ([]ChannelPolicy, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []ChannelPolicy
	for _, v := range s.channels {
		if v.OrganizationID == org {
			if organization, ok := s.organizations[org]; ok && organization.KillSwitch {
				v.KillSwitch = true
			}
			if workspace, ok := s.workspaces[org+"/"+v.TeamID]; ok {
				v.WorkspaceEnabled = workspace.Enabled
				v.KillSwitch = v.KillSwitch || !workspace.Enabled
			} else {
				v.WorkspaceEnabled = false
				v.KillSwitch = true
			}
			out = append(out, v)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ChannelID < out[j].ChannelID })
	return out, nil
}

type Mongo struct {
	db  *database.Database
	now func() time.Time
}

func NewMongo(db *database.Database) *Mongo { return &Mongo{db: db, now: time.Now} }

func (s *Mongo) GetOrganization(ctx context.Context, organizationID string) (models.Organization, error) {
	var value models.Organization
	err := s.db.Collection(models.CollectionOrganizations).FindOne(ctx, bson.M{"public_id": organizationID}).Decode(&value)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return models.Organization{}, ErrNotFound
	}
	if err != nil {
		return models.Organization{}, err
	}
	return value, nil
}

func (s *Mongo) ListOrganizations(ctx context.Context) ([]models.Organization, error) {
	cursor, err := s.db.Collection(models.CollectionOrganizations).Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "name", Value: 1}, {Key: "public_id", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var organizations []models.Organization
	if err := cursor.All(ctx, &organizations); err != nil {
		return nil, err
	}
	return organizations, nil
}

func (s *Mongo) GetWorkspace(ctx context.Context, organizationID, teamID string) (models.Workspace, error) {
	var value models.Workspace
	err := s.db.Collection(models.CollectionWorkspaces).FindOne(ctx, bson.M{"organization_id": organizationID, "team_id": teamID}).Decode(&value)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return models.Workspace{}, ErrNotFound
	}
	if err != nil {
		return models.Workspace{}, err
	}
	return value, nil
}

func (s *Mongo) PutOrganization(ctx context.Context, value models.Organization) (models.Organization, error) {
	if value.PublicID == "" || value.Name == "" || (value.EnrollmentMode != "allowlist" && value.EnrollmentMode != "all_observable_channels" && value.EnrollmentMode != "all_joined") {
		return models.Organization{}, fmt.Errorf("invalid organization")
	}
	now := s.now().UTC()
	after := options.After
	err := s.db.Collection(models.CollectionOrganizations).FindOneAndUpdate(ctx, bson.M{"public_id": value.PublicID}, bson.M{"$set": bson.M{"name": value.Name, "enrollment_mode": value.EnrollmentMode, "kill_switch": value.KillSwitch, "default_model_profile": value.DefaultModelProfile, "updated_at": now}, "$setOnInsert": bson.M{"public_id": value.PublicID, "created_at": now}, "$inc": bson.M{"version": 1}}, options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(after)).Decode(&value)
	return value, err
}
func (s *Mongo) PutWorkspace(ctx context.Context, value models.Workspace) (models.Workspace, error) {
	if value.OrganizationID == "" || value.TeamID == "" {
		return models.Workspace{}, fmt.Errorf("invalid workspace")
	}
	now := s.now().UTC()
	after := options.After
	err := s.db.Collection(models.CollectionWorkspaces).FindOneAndUpdate(ctx, bson.M{"organization_id": value.OrganizationID, "team_id": value.TeamID}, bson.M{"$set": bson.M{"name": value.Name, "enabled": value.Enabled, "updated_at": now}, "$setOnInsert": bson.M{"public_id": types.NewID("workspace"), "organization_id": value.OrganizationID, "team_id": value.TeamID, "created_at": now}, "$inc": bson.M{"version": 1}}, options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(after)).Decode(&value)
	return value, err
}
func (s *Mongo) PutChannel(ctx context.Context, value ChannelPolicy) (ChannelPolicy, error) {
	if err := ValidateChannel(value); err != nil {
		return ChannelPolicy{}, err
	}
	now := s.now().UTC()
	after := options.After
	var doc models.Channel
	value.ContextHistoryMode = normalizedContextHistoryMode(value.ContextHistoryMode)
	err := s.db.Collection(models.CollectionChannels).FindOneAndUpdate(ctx, bson.M{"organization_id": value.OrganizationID, "team_id": value.TeamID, "channel_id": value.ChannelID}, bson.M{"$set": bson.M{"name": value.Name, "enrolled": value.Enrolled, "restricted": value.Restricted, "participation_mode": string(value.ParticipationMode), "kill_switch": value.KillSwitch, "cooldown_seconds": int(value.Cooldown.Seconds()), "max_responses_per_hour": value.MaxResponsesPerHour, "max_concurrent_jobs": value.MaxConcurrentJobs, "default_model_profile": value.DefaultModelProfile, "context_history_mode": string(value.ContextHistoryMode), "approver_user_ids": value.ApproverUserIDs, "trusted_integration_bot_ids": value.TrustedIntegrationBotIDs, "bot_is_member": value.BotIsMember, "bot_membership_known": value.BotMembershipKnown, "participation_managed_by_membership": value.ParticipationManagedByMembership, "membership_revision": value.MembershipRevision, "membership_refreshed_at": value.MembershipRefreshedAt, "updated_at": now}, "$setOnInsert": bson.M{"public_id": types.NewID("channel"), "organization_id": value.OrganizationID, "team_id": value.TeamID, "channel_id": value.ChannelID, "created_at": now}, "$inc": bson.M{"version": 1}}, options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(after)).Decode(&doc)
	if err != nil {
		return ChannelPolicy{}, err
	}
	return channelFromModel(doc), nil
}

// UpsertContextChannel atomically refreshes Slack-derived membership fields.
// Enrollment, limits, model routing, and kill switches remain operator-owned;
// participation changes only when membership management is explicitly enabled.
func (s *Mongo) UpsertContextChannel(ctx context.Context, value ChannelPolicy) (ChannelPolicy, error) {
	if err := ValidateChannel(value); err != nil {
		return ChannelPolicy{}, err
	}
	now := s.now().UTC()
	if value.BotMembershipKnown && !value.ParticipationManagedByMembership {
		_, err := s.db.Collection(models.CollectionChannels).UpdateOne(ctx,
			bson.M{"organization_id": value.OrganizationID, "team_id": value.TeamID, "channel_id": value.ChannelID, "participation_managed_by_membership": true},
			bson.M{"$set": bson.M{"participation_mode": string(types.ModeObserve), "participation_managed_by_membership": false, "updated_at": now}, "$inc": bson.M{"version": 1}},
		)
		if err != nil {
			return ChannelPolicy{}, err
		}
	}
	set := bson.M{
		"membership_revision":     value.MembershipRevision,
		"membership_refreshed_at": value.MembershipRefreshedAt,
		"updated_at":              now,
	}
	if value.Name != "" {
		set["name"] = value.Name
	}
	if value.Restricted {
		set["restricted"] = true
	}
	if value.BotMembershipKnown {
		set["bot_is_member"] = value.BotIsMember
		set["bot_membership_known"] = true
	}
	if value.ParticipationManagedByMembership {
		set["participation_mode"] = string(value.ParticipationMode)
		set["participation_managed_by_membership"] = true
	}
	setOnInsert := bson.M{
		"public_id":                           types.NewID("channel"),
		"organization_id":                     value.OrganizationID,
		"team_id":                             value.TeamID,
		"channel_id":                          value.ChannelID,
		"enrolled":                            value.Enrolled,
		"restricted":                          value.Restricted,
		"participation_mode":                  string(value.ParticipationMode),
		"kill_switch":                         value.KillSwitch,
		"cooldown_seconds":                    int(value.Cooldown.Seconds()),
		"max_responses_per_hour":              value.MaxResponsesPerHour,
		"max_concurrent_jobs":                 value.MaxConcurrentJobs,
		"default_model_profile":               value.DefaultModelProfile,
		"context_history_mode":                string(normalizedContextHistoryMode(value.ContextHistoryMode)),
		"trusted_integration_bot_ids":         value.TrustedIntegrationBotIDs,
		"bot_is_member":                       value.BotIsMember,
		"bot_membership_known":                value.BotMembershipKnown,
		"participation_managed_by_membership": value.ParticipationManagedByMembership,
		"created_at":                          now,
	}
	if value.Name != "" {
		delete(setOnInsert, "name")
	} else {
		setOnInsert["name"] = ""
	}
	if value.Restricted {
		delete(setOnInsert, "restricted")
	}
	if value.BotMembershipKnown {
		delete(setOnInsert, "bot_is_member")
		delete(setOnInsert, "bot_membership_known")
	}
	if value.ParticipationManagedByMembership {
		delete(setOnInsert, "participation_mode")
		delete(setOnInsert, "participation_managed_by_membership")
	}
	var doc models.Channel
	err := s.db.Collection(models.CollectionChannels).FindOneAndUpdate(
		ctx,
		bson.M{"organization_id": value.OrganizationID, "team_id": value.TeamID, "channel_id": value.ChannelID},
		bson.M{"$set": set, "$setOnInsert": setOnInsert, "$inc": bson.M{"version": 1}},
		options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After),
	).Decode(&doc)
	if err != nil {
		return ChannelPolicy{}, err
	}
	return channelFromModel(doc), nil
}
func (s *Mongo) Resolve(ctx context.Context, org, team, channel string) (ChannelPolicy, error) {
	organization, err := s.GetOrganization(ctx, org)
	if err != nil {
		return ChannelPolicy{}, err
	}
	workspace, err := s.GetWorkspace(ctx, org, team)
	if err != nil {
		return ChannelPolicy{}, err
	}
	var doc models.Channel
	err = s.db.Collection(models.CollectionChannels).FindOne(ctx, bson.M{"organization_id": org, "team_id": team, "channel_id": channel}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return ChannelPolicy{}, ErrNotFound
	}
	if err != nil {
		return ChannelPolicy{}, err
	}
	policy := channelFromModel(doc)
	policy.WorkspaceEnabled = workspace.Enabled
	policy.KillSwitch = policy.KillSwitch || organization.KillSwitch || !workspace.Enabled
	return policy, nil
}
func (s *Mongo) ListChannels(ctx context.Context, org string) ([]ChannelPolicy, error) {
	organization, err := s.GetOrganization(ctx, org)
	if err != nil {
		return nil, err
	}
	cursor, err := s.db.Collection(models.CollectionChannels).Find(ctx, bson.M{"organization_id": org}, options.Find().SetSort(bson.D{{Key: "channel_id", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var docs []models.Channel
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, err
	}
	out := make([]ChannelPolicy, len(docs))
	workspaceEnabled := make(map[string]bool)
	workspaceCursor, err := s.db.Collection(models.CollectionWorkspaces).Find(ctx, bson.M{"organization_id": org})
	if err != nil {
		return nil, err
	}
	defer workspaceCursor.Close(ctx)
	var workspaces []models.Workspace
	if err := workspaceCursor.All(ctx, &workspaces); err != nil {
		return nil, err
	}
	for _, workspace := range workspaces {
		workspaceEnabled[workspace.TeamID] = workspace.Enabled
	}
	for i, d := range docs {
		out[i] = channelFromModel(d)
		enabled, exists := workspaceEnabled[d.TeamID]
		out[i].WorkspaceEnabled = enabled && exists
		out[i].KillSwitch = out[i].KillSwitch || organization.KillSwitch || !out[i].WorkspaceEnabled
	}
	return out, nil
}
func channelFromModel(d models.Channel) ChannelPolicy {
	return ChannelPolicy{OrganizationID: d.OrganizationID, TeamID: d.TeamID, ChannelID: d.ChannelID, Name: d.Name, Enrolled: d.Enrolled, Restricted: d.Restricted, ParticipationMode: types.ParticipationMode(d.ParticipationMode), KillSwitch: d.KillSwitch, Cooldown: time.Duration(d.CooldownSeconds) * time.Second, MaxResponsesPerHour: d.MaxResponsesPerHour, MaxConcurrentJobs: d.MaxConcurrentJobs, DefaultModelProfile: d.DefaultModelProfile, ContextHistoryMode: normalizedContextHistoryMode(types.ContextHistoryMode(d.ContextHistoryMode)), ApproverUserIDs: append([]string(nil), d.ApproverUserIDs...), TrustedIntegrationBotIDs: append([]string(nil), d.TrustedIntegrationBotIDs...), BotIsMember: d.BotIsMember, BotMembershipKnown: d.BotMembershipKnown, ParticipationManagedByMembership: d.ParticipationManagedByMembership, MembershipRevision: d.MembershipRevision, MembershipRefreshedAt: d.MembershipRefreshedAt, Version: d.Version}
}

func normalizedContextHistoryMode(mode types.ContextHistoryMode) types.ContextHistoryMode {
	if mode == "" {
		return types.ContextHistoryDurable
	}
	return mode
}
