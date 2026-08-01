package types

import "time"

type SlackSegmentKind string

const (
	SlackSegmentMRKDWN   SlackSegmentKind = "mrkdwn_text"
	SlackSegmentTable    SlackSegmentKind = "table"
	SlackSegmentArtifact SlackSegmentKind = "artifact"
	SlackSegmentApproval SlackSegmentKind = "approval"
	SlackSegmentNotice   SlackSegmentKind = "notice"
)

type SlackResult struct {
	Segments []SlackSegment `json:"segments"`
}

type SlackSegment struct {
	Kind     SlackSegmentKind `json:"kind"`
	Text     string           `json:"text,omitempty"`
	Table    *SlackTable      `json:"table,omitempty"`
	Artifact *SlackArtifact   `json:"artifact,omitempty"`
	Approval *SlackApproval   `json:"approval,omitempty"`
	Notice   *SlackNotice     `json:"notice,omitempty"`
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
	Columns []SlackTableColumn `json:"columns"`
	Rows    [][]SlackTableCell `json:"rows"`
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
