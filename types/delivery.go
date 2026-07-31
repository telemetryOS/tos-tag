package types

import "time"

type SlackSegmentKind string

const (
	SlackSegmentMRKDWN   SlackSegmentKind = "mrkdwn_text"
	SlackSegmentTable    SlackSegmentKind = "table"
	SlackSegmentArtifact SlackSegmentKind = "artifact"
)

type SlackResult struct {
	Segments []SlackSegment `json:"segments"`
}

type SlackSegment struct {
	Kind     SlackSegmentKind `json:"kind"`
	Text     string           `json:"text,omitempty"`
	Table    *SlackTable      `json:"table,omitempty"`
	Artifact *SlackArtifact   `json:"artifact,omitempty"`
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
