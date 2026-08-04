// Package models contains MongoDB persistence documents only.
package models

import (
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const (
	CollectionOrganizations          = "organizations"
	CollectionWorkspaces             = "workspaces"
	CollectionChannels               = "channels"
	CollectionObservations           = "channel_observations"
	CollectionMessages               = "channel_messages"
	CollectionChannelCounters        = "channel_receive_counters"
	CollectionOrganizationCounts     = "organization_receive_counters"
	CollectionDecisions              = "classifier_decisions"
	CollectionContextPacks           = "context_pack_revisions"
	CollectionSituationFacts         = "situation_facts"
	CollectionRestrictedSignals      = "restricted_signals"
	CollectionSummaries              = "summaries"
	CollectionProjectorWatermarks    = "projector_watermarks"
	CollectionDerivations            = "source_derivations"
	CollectionSessions               = "sessions"
	CollectionGenerations            = "session_generations"
	CollectionJobs                   = "jobs"
	CollectionAttempts               = "job_attempts"
	CollectionDeliveries             = "slack_deliveries"
	CollectionReceipts               = "event_receipts"
	CollectionAuditHeads             = "audit_chain_heads"
	CollectionModelProfiles          = "model_profiles"
	CollectionModelRules             = "model_routing_rules"
	CollectionDirectives             = "channel_directives"
	CollectionDirectiveRevisions     = "channel_directive_revisions"
	CollectionNotes                  = "channel_notes"
	CollectionNoteRevisions          = "channel_note_revisions"
	CollectionUsage                  = "usage_events"
	CollectionSecrets                = "secret_references"
	CollectionAdmissionStates        = "admission_states"
	CollectionAdmissionReservations  = "admission_reservations"
	CollectionClassifierFloodBuckets = "classifier_flood_buckets"
	CollectionApprovals              = "tool_approvals"
	CollectionRoutines               = "routines"
	CollectionEventSubscriptions     = "event_subscriptions"
	CollectionSlackContextSync       = "slack_context_sync_states"
)

type Observation struct {
	ID                      bson.ObjectID `bson:"_id,omitempty"`
	PublicID                string        `bson:"public_id"`
	OrganizationID          string        `bson:"organization_id"`
	TeamID                  string        `bson:"team_id"`
	ChannelID               string        `bson:"channel_id"`
	EventID                 string        `bson:"event_id"`
	EnvelopeID              string        `bson:"envelope_id"`
	ReceivedSeq             int64         `bson:"received_seq"`
	OrganizationReceivedSeq int64         `bson:"organization_received_seq"`
	SlackEventTime          time.Time     `bson:"slack_event_time"`
	ReceivedAt              time.Time     `bson:"received_at"`
	MessageTS               string        `bson:"message_ts"`
	RootThreadTS            string        `bson:"root_thread_ts"`
	UserID                  string        `bson:"user_id,omitempty"`
	BotID                   string        `bson:"bot_id,omitempty"`
	Restricted              bool          `bson:"restricted"`
	IsMention               bool          `bson:"is_mention"`
	OriginTag               string        `bson:"origin_tag,omitempty"`
	EventType               string        `bson:"event_type"`
	Subtype                 string        `bson:"subtype,omitempty"`
	Text                    string        `bson:"text,omitempty"`
	MutationTargetTS        string        `bson:"mutation_target_ts,omitempty"`
	ScopeState              string        `bson:"scope_state"`
	DecisionState           string        `bson:"decision_state"`
	DecisionLeaseOwner      string        `bson:"decision_lease_owner,omitempty"`
	DecisionLeaseToken      string        `bson:"decision_lease_token,omitempty"`
	DecisionLeaseExpiresAt  time.Time     `bson:"decision_lease_expires_at,omitempty"`
	OutputProduced          bool          `bson:"output_produced"`
	OutputReservationID     string        `bson:"output_reservation_id,omitempty"`
	OutputJobID             string        `bson:"output_job_id,omitempty"`
	OutputDeliveryID        string        `bson:"output_delivery_id,omitempty"`
	CreatedAt               time.Time     `bson:"created_at"`
	ExpiresAt               time.Time     `bson:"expires_at"`
	Version                 int64         `bson:"version"`
}

type ChannelMessage struct {
	ID                bson.ObjectID `bson:"_id,omitempty"`
	OrganizationID    string        `bson:"organization_id"`
	TeamID            string        `bson:"team_id"`
	ChannelID         string        `bson:"channel_id"`
	MessageTS         string        `bson:"message_ts"`
	RootThreadTS      string        `bson:"root_thread_ts"`
	AuthorID          string        `bson:"author_id,omitempty"`
	BotID             string        `bson:"bot_id,omitempty"`
	Subtype           string        `bson:"subtype,omitempty"`
	Text              string        `bson:"text,omitempty"`
	Deleted           bool          `bson:"deleted"`
	Restricted        bool          `bson:"restricted"`
	SourceEventID     string        `bson:"source_event_id"`
	SourceEventAt     time.Time     `bson:"source_event_at"`
	SourceEventRank   int           `bson:"source_event_rank"`
	ProjectionVersion int64         `bson:"projection_version"`
	OriginalAt        time.Time     `bson:"original_at"`
	UpdatedAt         time.Time     `bson:"updated_at"`
	ExpiresAt         time.Time     `bson:"expires_at"`
}

type Counter struct {
	ID        string    `bson:"_id"`
	Sequence  int64     `bson:"sequence"`
	UpdatedAt time.Time `bson:"updated_at"`
}

type Organization struct {
	ID                  bson.ObjectID `bson:"_id,omitempty"`
	PublicID            string        `bson:"public_id"`
	Name                string        `bson:"name"`
	EnrollmentMode      string        `bson:"enrollment_mode"`
	KillSwitch          bool          `bson:"kill_switch"`
	DefaultModelProfile string        `bson:"default_model_profile,omitempty"`
	CreatedAt           time.Time     `bson:"created_at"`
	UpdatedAt           time.Time     `bson:"updated_at"`
	Version             int64         `bson:"version"`
}

type Workspace struct {
	ID             bson.ObjectID `bson:"_id,omitempty"`
	PublicID       string        `bson:"public_id"`
	OrganizationID string        `bson:"organization_id"`
	TeamID         string        `bson:"team_id"`
	Name           string        `bson:"name"`
	Enabled        bool          `bson:"enabled"`
	CreatedAt      time.Time     `bson:"created_at"`
	UpdatedAt      time.Time     `bson:"updated_at"`
	Version        int64         `bson:"version"`
}

type Channel struct {
	ID                               bson.ObjectID `bson:"_id,omitempty"`
	PublicID                         string        `bson:"public_id"`
	OrganizationID                   string        `bson:"organization_id"`
	TeamID                           string        `bson:"team_id"`
	ChannelID                        string        `bson:"channel_id"`
	Name                             string        `bson:"name"`
	Enrolled                         bool          `bson:"enrolled"`
	Restricted                       bool          `bson:"restricted"`
	ParticipationMode                string        `bson:"participation_mode"`
	KillSwitch                       bool          `bson:"kill_switch"`
	CooldownSeconds                  int           `bson:"cooldown_seconds"`
	MaxResponsesPerHour              int           `bson:"max_responses_per_hour"`
	MaxConcurrentJobs                int           `bson:"max_concurrent_jobs"`
	DefaultModelProfile              string        `bson:"default_model_profile,omitempty"`
	ContextHistoryMode               string        `bson:"context_history_mode,omitempty"`
	ApproverUserIDs                  []string      `bson:"approver_user_ids,omitempty"`
	BotIsMember                      bool          `bson:"bot_is_member"`
	BotMembershipKnown               bool          `bson:"bot_membership_known"`
	ParticipationManagedByMembership bool          `bson:"participation_managed_by_membership"`
	MembershipRevision               string        `bson:"membership_revision"`
	MembershipRefreshedAt            time.Time     `bson:"membership_refreshed_at"`
	CreatedAt                        time.Time     `bson:"created_at"`
	UpdatedAt                        time.Time     `bson:"updated_at"`
	Version                          int64         `bson:"version"`
}

// SlackContextSyncState records durable history-bootstrap progress separately
// from retained message content. Message TTL expiry must not cause a completed
// conversation to be fully fetched again on every process restart.
type SlackContextSyncState struct {
	ID                   bson.ObjectID             `bson:"_id,omitempty"`
	OrganizationID       string                    `bson:"organization_id"`
	TeamID               string                    `bson:"team_id"`
	ChannelID            string                    `bson:"channel_id"`
	BootstrapCompleted   bool                      `bson:"bootstrap_completed"`
	BootstrapCompletedAt time.Time                 `bson:"bootstrap_completed_at,omitempty"`
	SyncedThrough        time.Time                 `bson:"synced_through,omitempty"`
	CatchUpThrough       time.Time                 `bson:"catch_up_through,omitempty"`
	CatchUpLatest        time.Time                 `bson:"catch_up_latest,omitempty"`
	CatchUpThreads       []SlackThreadCatchUpState `bson:"catch_up_threads,omitempty"`
	LiveThrough          time.Time                 `bson:"live_through,omitempty"`
	UpdatedAt            time.Time                 `bson:"updated_at"`
}

// SlackThreadCatchUpState is a content-free per-thread checkpoint used while
// repairing a bounded offline gap. RootThreadTS is a Slack identifier, not
// message content.
type SlackThreadCatchUpState struct {
	RootThreadTS  string    `bson:"root_thread_ts"`
	SyncedThrough time.Time `bson:"synced_through"`
}

type ContextPack struct {
	ID                  bson.ObjectID `bson:"_id,omitempty"`
	PublicID            string        `bson:"public_id"`
	OrganizationID      string        `bson:"organization_id"`
	TargetObservationID string        `bson:"target_observation_id"`
	Revision            int64         `bson:"revision"`
	Payload             any           `bson:"payload"`
	SourceIDs           []string      `bson:"source_ids"`
	CreatedAt           time.Time     `bson:"created_at"`
	ExpiresAt           time.Time     `bson:"expires_at"`
}

type SituationFact struct {
	ID              bson.ObjectID `bson:"_id,omitempty"`
	PublicID        string        `bson:"public_id"`
	OrganizationID  string        `bson:"organization_id"`
	Kind            string        `bson:"kind"`
	Status          string        `bson:"status"`
	Summary         string        `bson:"summary,omitempty"`
	SourceIDs       []string      `bson:"source_ids"`
	ChannelID       string        `bson:"channel_id"`
	MessageTS       string        `bson:"message_ts"`
	SourceExpiresAt time.Time     `bson:"source_expires_at"`
	UpdatedAt       time.Time     `bson:"updated_at"`
	ExpiresAt       time.Time     `bson:"expires_at"`
}

type RestrictedSignal struct {
	ID             bson.ObjectID `bson:"_id,omitempty"`
	PublicID       string        `bson:"public_id"`
	OrganizationID string        `bson:"organization_id"`
	Kind           string        `bson:"kind"`
	Active         bool          `bson:"active"`
	SourceID       string        `bson:"source_id"`
	ChannelID      string        `bson:"channel_id"`
	MessageTS      string        `bson:"message_ts"`
	CreatedAt      time.Time     `bson:"created_at"`
	ExpiresAt      time.Time     `bson:"expires_at"`
}

type Summary struct {
	ID             bson.ObjectID `bson:"_id,omitempty"`
	PublicID       string        `bson:"public_id"`
	OrganizationID string        `bson:"organization_id"`
	ChannelID      string        `bson:"channel_id,omitempty"`
	Text           string        `bson:"text"`
	SourceIDs      []string      `bson:"source_ids"`
	CreatedAt      time.Time     `bson:"created_at"`
	ExpiresAt      time.Time     `bson:"expires_at"`
}

type SourceDerivation struct {
	ID                bson.ObjectID `bson:"_id,omitempty"`
	OrganizationID    string        `bson:"organization_id"`
	SourceID          string        `bson:"source_id"`
	DerivedCollection string        `bson:"derived_collection"`
	DerivedID         string        `bson:"derived_id"`
	CreatedAt         time.Time     `bson:"created_at"`
	ExpiresAt         time.Time     `bson:"expires_at"`
}

type ProjectorWatermark struct {
	ID             string    `bson:"_id"`
	OrganizationID string    `bson:"organization_id"`
	Sequence       int64     `bson:"sequence"`
	ObservedAt     time.Time `bson:"observed_at"`
	UpdatedAt      time.Time `bson:"updated_at"`
}

type Lease struct {
	Owner     string    `bson:"owner"`
	Token     string    `bson:"token"`
	ExpiresAt time.Time `bson:"expires_at"`
	Heartbeat time.Time `bson:"heartbeat_at"`
}

type Job struct {
	ID                     bson.ObjectID `bson:"_id,omitempty"`
	PublicID               string        `bson:"public_id"`
	OrganizationID         string        `bson:"organization_id"`
	WorkspaceID            string        `bson:"workspace_id"`
	ChannelID              string        `bson:"channel_id"`
	RootThreadTS           string        `bson:"root_thread_ts"`
	ReplyInChannel         bool          `bson:"reply_in_channel,omitempty"`
	SessionID              string        `bson:"session_id"`
	Generation             int64         `bson:"generation"`
	ObservationID          string        `bson:"observation_id,omitempty"`
	RequesterID            string        `bson:"requester_id,omitempty"`
	IdempotencyKey         string        `bson:"idempotency_key"`
	Kind                   string        `bson:"kind"`
	Input                  string        `bson:"input"`
	State                  string        `bson:"state"`
	Attempt                int           `bson:"attempt"`
	MaxAttempts            int           `bson:"max_attempts"`
	AdmissionReservationID string        `bson:"admission_reservation_id,omitempty"`
	ResolvedModel          any           `bson:"resolved_model,omitempty"`
	RouteTrace             any           `bson:"route_trace,omitempty"`
	SteeringEpoch          int64         `bson:"steering_epoch"`
	Lease                  Lease         `bson:"lease"`
	Result                 any           `bson:"result,omitempty"`
	FailureReason          string        `bson:"failure_reason,omitempty"`
	ApprovalID             string        `bson:"approval_id,omitempty"`
	ApprovedActionHash     string        `bson:"approved_action_hash,omitempty"`
	ProgressMessageTS      string        `bson:"progress_message_ts,omitempty"`
	FinalDeliveryEnqueued  bool          `bson:"final_delivery_enqueued,omitempty"`
	WriterActive           bool          `bson:"writer_active"`
	AvailableAt            time.Time     `bson:"available_at"`
	CreatedAt              time.Time     `bson:"created_at"`
	UpdatedAt              time.Time     `bson:"updated_at"`
	ExpiresAt              time.Time     `bson:"expires_at"`
	Version                int64         `bson:"version"`
}

type Session struct {
	ID                bson.ObjectID `bson:"_id,omitempty"`
	PublicID          string        `bson:"public_id"`
	OrganizationID    string        `bson:"organization_id"`
	TeamID            string        `bson:"team_id"`
	ChannelID         string        `bson:"channel_id"`
	RootThreadTS      string        `bson:"root_thread_ts"`
	CurrentGeneration int64         `bson:"current_generation"`
	CreatedAt         time.Time     `bson:"created_at"`
	UpdatedAt         time.Time     `bson:"updated_at"`
	Version           int64         `bson:"version"`
}

type Delivery struct {
	ID             bson.ObjectID `bson:"_id,omitempty"`
	PublicID       string        `bson:"public_id"`
	OrganizationID string        `bson:"organization_id"`
	JobID          string        `bson:"job_id"`
	DecisionID     string        `bson:"decision_id,omitempty"`
	IdempotencyKey string        `bson:"idempotency_key"`
	TeamID         string        `bson:"team_id"`
	ChannelID      string        `bson:"channel_id"`
	ThreadTS       string        `bson:"thread_ts,omitempty"`
	UpdateTS       string        `bson:"update_ts,omitempty"`
	StreamTS       string        `bson:"stream_ts,omitempty"`
	Result         any           `bson:"result"`
	Status         string        `bson:"status"`
	Attempt        int           `bson:"attempt"`
	MaxAttempts    int           `bson:"max_attempts"`
	RetryAt        time.Time     `bson:"retry_at"`
	Lease          Lease         `bson:"lease"`
	SlackMessageTS string        `bson:"slack_message_ts,omitempty"`
	FailureReason  string        `bson:"failure_reason,omitempty"`
	CreatedAt      time.Time     `bson:"created_at"`
	UpdatedAt      time.Time     `bson:"updated_at"`
	ExpiresAt      time.Time     `bson:"expires_at"`
	Version        int64         `bson:"version"`
}

type ClassificationDecision struct {
	ID                    bson.ObjectID `bson:"_id,omitempty"`
	PublicID              string        `bson:"public_id"`
	OrganizationID        string        `bson:"organization_id"`
	ObservationID         string        `bson:"observation_id"`
	DecisionRevision      int64         `bson:"decision_revision"`
	ContextPackRevisionID string        `bson:"context_pack_revision_id"`
	OrganizationWatermark int64         `bson:"organization_watermark"`
	Predicted             any           `bson:"predicted"`
	Effective             any           `bson:"effective"`
	Shadowed              bool          `bson:"shadowed"`
	CreatedAt             time.Time     `bson:"created_at"`
}
