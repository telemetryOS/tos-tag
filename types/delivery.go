package types

import "time"

type SlackSegmentKind string

const (
	SlackSegmentHeader   SlackSegmentKind = "header"
	SlackSegmentMRKDWN   SlackSegmentKind = "mrkdwn_text"
	SlackSegmentContext  SlackSegmentKind = "context"
	SlackSegmentDivider  SlackSegmentKind = "divider"
	SlackSegmentTable    SlackSegmentKind = "table"
	SlackSegmentCard     SlackSegmentKind = "card"
	SlackSegmentCarousel SlackSegmentKind = "carousel"
	SlackSegmentImage    SlackSegmentKind = "image"
	SlackSegmentArtifact SlackSegmentKind = "artifact"
	SlackSegmentApproval SlackSegmentKind = "approval"
	SlackSegmentNotice   SlackSegmentKind = "notice"
)

type SlackResult struct {
	Segments        []SlackSegment        `json:"segments" bson:"segments"`
	AllowedMentions SlackMentionAllowlist `json:"-" bson:"allowed_mentions,omitempty"`
	AgentFooter     *SlackAgentFooter     `json:"-" bson:"agent_footer,omitempty"`
}

// SlackMentionAllowlist is control-plane-owned provenance attached after model
// output parsing. JSON model output cannot set or broaden it.
type SlackMentionAllowlist struct {
	UserIDs    []string `json:"-" bson:"user_ids,omitempty"`
	ChannelIDs []string `json:"-" bson:"channel_ids,omitempty"`
}

// SlackAgentFooter is control-plane-owned execution metadata for a full-agent
// response. Model JSON cannot set it, and classifier-only replies omit it.
type SlackAgentFooter struct {
	ModelID               string `json:"-" bson:"model_id,omitempty"`
	ReasoningEffort       string `json:"-" bson:"reasoning_effort,omitempty"`
	InputTokens           int64  `json:"-" bson:"input_tokens,omitempty"`
	OutputTokens          int64  `json:"-" bson:"output_tokens,omitempty"`
	CachedInputTokens     int64  `json:"-" bson:"cached_input_tokens,omitempty"`
	ReasoningOutputTokens int64  `json:"-" bson:"reasoning_output_tokens,omitempty"`
	TotalTokens           int64  `json:"-" bson:"total_tokens,omitempty"`
	DurationMS            int64  `json:"-" bson:"duration_ms,omitempty"`
}

type SlackSegment struct {
	Kind     SlackSegmentKind `json:"kind"`
	Text     string           `json:"text,omitempty"`
	Table    *SlackTable      `json:"table,omitempty"`
	Card     *SlackCard       `json:"card,omitempty"`
	Carousel *SlackCarousel   `json:"carousel,omitempty"`
	Image    *SlackImage      `json:"image,omitempty"`
	Artifact *SlackArtifact   `json:"artifact,omitempty"`
	Approval *SlackApproval   `json:"approval,omitempty"`
	Notice   *SlackNotice     `json:"notice,omitempty"`
}

// SlackCard is the model-safe subset of Slack's native Card block. Actions are
// intentionally absent: interactive controls remain control-plane owned.
type SlackCard struct {
	Title     string          `json:"title"`
	Subtitle  string          `json:"subtitle,omitempty"`
	Body      string          `json:"body"`
	Subtext   string          `json:"subtext,omitempty"`
	Icon      *SlackCardImage `json:"icon,omitempty"`
	HeroImage *SlackCardImage `json:"hero_image,omitempty"`
}

type SlackCardImage struct {
	URL     string `json:"url"`
	AltText string `json:"alt_text"`
}

type SlackCarousel struct {
	Cards []SlackCard `json:"cards"`
}

type SlackImage struct {
	URL     string `json:"url"`
	AltText string `json:"alt_text"`
	Title   string `json:"title,omitempty"`
}

type SlackApproval struct {
	ID          string         `json:"id"`
	ActionHash  string         `json:"action_hash"`
	ToolID      string         `json:"tool_id"`
	OperationID string         `json:"operation_id"`
	Risk        string         `json:"risk"`
	Destination string         `json:"destination"`
	Arguments   map[string]any `json:"arguments"`
	ExpiresAt   time.Time      `json:"expires_at"`
	Status      string         `json:"status,omitempty"`
	ResolvedAt  time.Time      `json:"resolved_at,omitempty"`
}

type SlackNotice struct {
	Tone    string `json:"tone"`
	Title   string `json:"title"`
	Message string `json:"message"`
	Context string `json:"context,omitempty"`
}

type SlackTable struct {
	Columns              []SlackTableColumn `json:"columns"`
	Rows                 [][]SlackTableCell `json:"rows"`
	Caption              string             `json:"caption,omitempty"`
	PageSize             int                `json:"page_size,omitempty"`
	RowHeaderColumnIndex int                `json:"row_header_column_index,omitempty"`
}

type SlackTableColumn struct {
	Header  string `json:"header"`
	Align   string `json:"align,omitempty"`
	Wrapped bool   `json:"wrapped,omitempty"`
}

type SlackTableCell struct {
	Type   string  `json:"type"`
	Text   string  `json:"text,omitempty"`
	Number float64 `json:"number,omitempty"`
}

type SlackArtifact struct {
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	URL       string `json:"url"`
}

type SlackDestination struct {
	TeamID    string `json:"team_id"`
	ChannelID string `json:"channel_id"`
	ThreadTS  string `json:"thread_ts,omitempty"`
	UpdateTS  string `json:"update_ts,omitempty"`
	StreamTS  string `json:"stream_ts,omitempty"`
}

type SlackDeliveryRequest struct {
	ID             DeliveryID       `json:"id"`
	IdempotencyKey string           `json:"idempotency_key"`
	Destination    SlackDestination `json:"destination"`
	Result         SlackResult      `json:"result"`
}

type SlackDeliveryResult struct {
	MessageTS   string    `json:"message_ts"`
	DeliveredAt time.Time `json:"delivered_at"`
	Duplicate   bool      `json:"duplicate"`
}

type SlackReactionRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
	TeamID         string `json:"team_id"`
	ChannelID      string `json:"channel_id"`
	MessageTS      string `json:"message_ts"`
	Emoji          string `json:"emoji"`
}

type SlackReactionResult struct {
	AppliedAt time.Time `json:"applied_at"`
	Duplicate bool      `json:"duplicate"`
}

type SlackProgressStatus string

const (
	SlackProgressPending    SlackProgressStatus = "pending"
	SlackProgressInProgress SlackProgressStatus = "in_progress"
	SlackProgressComplete   SlackProgressStatus = "complete"
	SlackProgressError      SlackProgressStatus = "error"
)

// SlackProgressStep is a safe operational summary for Slack's Thinking Steps
// timeline. It must never contain hidden reasoning, tool arguments, raw model
// output, credentials, or private context.
type SlackProgressStep struct {
	ID      string                `json:"id"`
	Title   string                `json:"title"`
	Status  SlackProgressStatus   `json:"status"`
	Details string                `json:"details,omitempty"`
	Output  string                `json:"output,omitempty"`
	Sources []SlackProgressSource `json:"sources,omitempty"`
}

type SlackProgressSource struct {
	URL  string `json:"url"`
	Text string `json:"text"`
}

type SlackProgressStartRequest struct {
	IdempotencyKey  string            `json:"idempotency_key"`
	TeamID          string            `json:"team_id"`
	ChannelID       string            `json:"channel_id"`
	ThreadTS        string            `json:"thread_ts,omitempty"`
	JobID           JobID             `json:"job_id"`
	RecipientUserID string            `json:"recipient_user_id"`
	Title           string            `json:"title"`
	Step            SlackProgressStep `json:"step"`
}

type SlackProgressUpdateRequest struct {
	TeamID    string            `json:"team_id"`
	ChannelID string            `json:"channel_id"`
	MessageTS string            `json:"message_ts"`
	JobID     JobID             `json:"job_id"`
	Step      SlackProgressStep `json:"step"`
}

type SlackProgressResult struct {
	MessageTS string    `json:"message_ts"`
	UpdatedAt time.Time `json:"updated_at"`
	Duplicate bool      `json:"duplicate"`
}
