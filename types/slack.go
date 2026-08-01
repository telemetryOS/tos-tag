package types

import "time"

type SlackEventKind string

const (
	SlackEventMessage SlackEventKind = "message"
	SlackEventEdit    SlackEventKind = "message_edit"
	SlackEventDelete  SlackEventKind = "message_delete"
)

type SlackEnvelope struct {
	OrganizationID string         `json:"organization_id"`
	EnvelopeID     string         `json:"envelope_id"`
	EventID        string         `json:"event_id"`
	TeamID         string         `json:"team_id"`
	ChannelID      string         `json:"channel_id"`
	MessageTS      string         `json:"message_ts"`
	ThreadTS       string         `json:"thread_ts,omitempty"`
	UserID         string         `json:"user_id,omitempty"`
	BotID          string         `json:"bot_id,omitempty"`
	Kind           SlackEventKind `json:"kind"`
	Subtype        string         `json:"subtype,omitempty"`
	Text           string         `json:"text,omitempty"`
	TargetTS       string         `json:"target_ts,omitempty"`
	EventTime      time.Time      `json:"event_time"`
	ReceivedAt     time.Time      `json:"received_at"`
	IsMention      bool           `json:"is_mention"`
	OriginTag      string         `json:"origin_tag,omitempty"`
	Restricted     bool           `json:"restricted"`
}

func (e SlackEnvelope) RootThreadTS() string {
	if e.ThreadTS != "" {
		return e.ThreadTS
	}
	return e.MessageTS
}

type SlackAck struct {
	EnvelopeID string    `json:"envelope_id"`
	AcceptedAt time.Time `json:"accepted_at"`
	Duplicate  bool      `json:"duplicate"`
}

// SlackContextChannel is user-authorized Slack conversation metadata used to
// establish a context-only channel policy before importing retained history.
// Restricted covers private channels, DMs, and MPIMs.
type SlackContextChannel struct {
	OrganizationID   string `json:"organization_id"`
	TeamID           string `json:"team_id"`
	ChannelID        string `json:"channel_id"`
	Name             string `json:"name,omitempty"`
	Restricted       bool   `json:"restricted"`
	RestrictionKnown bool   `json:"restriction_known,omitempty"`
}
