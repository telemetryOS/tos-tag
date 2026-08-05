package types

import "time"

type ContextHistoryMode string

const (
	ContextHistoryDurable     ContextHistoryMode = "durable"
	ContextHistorySessionOnly ContextHistoryMode = "session_only"
)

type SlackEventKind string

const (
	SlackEventMessage SlackEventKind = "message"
	SlackEventEdit    SlackEventKind = "message_edit"
	SlackEventDelete  SlackEventKind = "message_delete"
)

const (
	SlackMessageSubtypeBotMessage         = "bot_message"
	SlackMessageSubtypeAssistantAppThread = "assistant_app_thread"
	SlackMessageSubtypeChannelJoin        = "channel_join"
	SlackMessageSubtypeChannelLeave       = "channel_leave"
	SlackMessageSubtypeGroupJoin          = "group_join"
	SlackMessageSubtypeGroupLeave         = "group_leave"
)

// SlackChannelKindGroupDM identifies multi-party direct messages (Slack
// channel_type "mpim"). tos-tag ignores these conversations entirely: they are
// excluded from discovery, live persistence, and channel coverage.
const SlackChannelKindGroupDM = "mpim"

// SlackChannelKindDirectMessage identifies one-to-one direct messages (Slack
// channel_type "im"). Unlike group DMs, DMs are automatically enrolled as
// assist destinations and a human message is inherently addressed to Tag.
const SlackChannelKindDirectMessage = "im"

type SlackEnvelope struct {
	OrganizationID string          `json:"organization_id"`
	EnvelopeID     string          `json:"envelope_id"`
	EventID        string          `json:"event_id"`
	TeamID         string          `json:"team_id"`
	ChannelID      string          `json:"channel_id"`
	ChannelKind    string          `json:"channel_kind,omitempty"`
	MessageTS      string          `json:"message_ts"`
	ThreadTS       string          `json:"thread_ts,omitempty"`
	UserID         string          `json:"user_id,omitempty"`
	BotID          string          `json:"bot_id,omitempty"`
	Kind           SlackEventKind  `json:"kind"`
	Subtype        string          `json:"subtype,omitempty"`
	Text           string          `json:"text,omitempty"`
	Images         []SlackImageRef `json:"images,omitempty"`
	TargetTS       string          `json:"target_ts,omitempty"`
	EventTime      time.Time       `json:"event_time"`
	ReceivedAt     time.Time       `json:"received_at"`
	IsMention      bool            `json:"is_mention"`
	OriginTag      string          `json:"origin_tag,omitempty"`
	Restricted     bool            `json:"restricted"`
}

// SlackImageRef is the bounded, non-secret identity of an image attached to a
// Slack message. Private download URLs and bot credentials are never persisted;
// the control plane resolves FileID through the authenticated Slack client only
// when an admitted job is ready to run.
type SlackImageRef struct {
	FileID    string `json:"file_id" bson:"file_id"`
	Name      string `json:"name,omitempty" bson:"name,omitempty"`
	MediaType string `json:"media_type" bson:"media_type"`
	Size      int    `json:"size,omitempty" bson:"size,omitempty"`
	Width     int    `json:"width,omitempty" bson:"width,omitempty"`
	Height    int    `json:"height,omitempty" bson:"height,omitempty"`
}

type SlackImageData struct {
	Name      string `json:"-"`
	MediaType string `json:"-"`
	Data      []byte `json:"-"`
}

func (e SlackEnvelope) RootThreadTS() string {
	if e.ThreadTS != "" {
		return e.ThreadTS
	}
	return e.MessageTS
}

// IntegrationAuthored reports whether Slack identified the message as coming
// from an app, bot, workflow, or assistant rather than a human. These messages
// are useful as untrusted conversational context, but must never trigger Tag:
// suppressing them before classification prevents agent-to-agent reply loops.
func (e SlackEnvelope) IntegrationAuthored() bool {
	return IsSlackIntegrationMessage(e.BotID, e.Subtype)
}

func IsSlackIntegrationMessage(botID, subtype string) bool {
	if botID != "" {
		return true
	}
	switch subtype {
	case SlackMessageSubtypeBotMessage, SlackMessageSubtypeAssistantAppThread:
		return true
	default:
		return false
	}
}

// MembershipEvent reports Slack's non-conversational join/leave messages.
// They are useful as local channel context, but must never become classifier
// input or an invocation grant—even when nearby context describes an incident.
func (e SlackEnvelope) MembershipEvent() bool {
	switch e.Subtype {
	case SlackMessageSubtypeChannelJoin, SlackMessageSubtypeChannelLeave, SlackMessageSubtypeGroupJoin, SlackMessageSubtypeGroupLeave:
		return true
	default:
		return false
	}
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
	OrganizationID     string `json:"organization_id"`
	TeamID             string `json:"team_id"`
	ChannelID          string `json:"channel_id"`
	Name               string `json:"name,omitempty"`
	Restricted         bool   `json:"restricted"`
	RestrictionKnown   bool   `json:"restriction_known,omitempty"`
	IsChannel          bool   `json:"is_channel"`
	BotIsMember        bool   `json:"bot_is_member"`
	BotMembershipKnown bool   `json:"bot_membership_known"`
}
