package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/telemetryos/tos-tag/core/deliveries"
	"github.com/telemetryos/tos-tag/types"
)

type LiveOptions struct {
	OrganizationID string
	TeamID         string
	AppToken       string
	BotToken       string
	BotUserID      string
}

type LiveIngress struct {
	options LiveOptions
	client  *socketmode.Client

	mu      sync.Mutex
	cancel  context.CancelFunc
	done    chan struct{}
	started bool
}

type deliveryAPI interface {
	PostMessageContext(context.Context, string, ...slackapi.MsgOption) (string, string, error)
	GetConversationHistoryContext(context.Context, *slackapi.GetConversationHistoryParameters) (*slackapi.GetConversationHistoryResponse, error)
	GetConversationRepliesContext(context.Context, *slackapi.GetConversationRepliesParameters) ([]slackapi.Message, bool, string, error)
}

type LiveDelivery struct {
	teamID   string
	api      deliveryAPI
	renderer *deliveries.Renderer
}

// NewLive constructs the production adapters without opening a connection.
// Start is the only method that initiates Socket Mode network activity.
func NewLive(options LiveOptions, renderer *deliveries.Renderer) (*LiveIngress, *LiveDelivery, error) {
	if options.OrganizationID == "" || options.TeamID == "" || !strings.HasPrefix(options.AppToken, "xapp-") || !strings.HasPrefix(options.BotToken, "xoxb-") {
		return nil, nil, errors.New("invalid live Slack options")
	}
	if renderer == nil {
		return nil, nil, errors.New("Slack renderer is required")
	}
	api := slackapi.New(options.BotToken, slackapi.OptionAppLevelToken(options.AppToken))
	client := socketmode.New(api)
	return &LiveIngress{options: options, client: client}, &LiveDelivery{teamID: options.TeamID, api: api, renderer: renderer}, nil
}

func (s *LiveIngress) Start(parent context.Context, handler Handler) error {
	if handler == nil {
		return errors.New("Slack handler is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started {
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	s.cancel = cancel
	s.done = make(chan struct{})
	s.started = true
	go s.run(ctx, handler)
	return nil
}

func (s *LiveIngress) run(ctx context.Context, handler Handler) {
	defer close(s.done)
	runDone := make(chan error, 1)
	go func() { runDone <- s.client.RunContext(ctx) }()
	for {
		select {
		case <-ctx.Done():
			<-runDone
			return
		case <-runDone:
			return
		case event := <-s.client.Events:
			if event.Type != socketmode.EventTypeEventsAPI || event.Request == nil {
				continue
			}
			normalized, eligible, err := NormalizeEventsAPI(s.options.OrganizationID, s.options.BotUserID, event.Request.EnvelopeID, event.Data)
			if err != nil {
				continue
			}
			if !eligible {
				_ = s.client.AckCtx(ctx, event.Request.EnvelopeID, nil)
				continue
			}
			if _, err := handler(ctx, normalized); err != nil {
				// Deliberately do not acknowledge: Slack may retry after a
				// persistence or admission failure.
				continue
			}
			_ = s.client.AckCtx(ctx, event.Request.EnvelopeID, nil)
		}
	}
}

func (s *LiveIngress) Stop(ctx context.Context) error {
	s.mu.Lock()
	if !s.started {
		s.mu.Unlock()
		return nil
	}
	cancel, done := s.cancel, s.done
	s.started = false
	s.mu.Unlock()
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func NormalizeEventsAPI(organizationID, botUserID, envelopeID string, data any) (types.SlackEnvelope, bool, error) {
	event, ok := data.(slackevents.EventsAPIEvent)
	if !ok || event.Type != slackevents.CallbackEvent {
		return types.SlackEnvelope{}, false, nil
	}
	callback, ok := event.Data.(slackevents.EventsAPICallbackEvent)
	if !ok {
		return types.SlackEnvelope{}, false, errors.New("Slack callback payload is malformed")
	}
	base := types.SlackEnvelope{
		OrganizationID: organizationID,
		EnvelopeID:     envelopeID,
		EventID:        callback.EventID,
		TeamID:         callback.TeamID,
		EventTime:      time.Unix(int64(callback.EventTime), 0).UTC(),
		ReceivedAt:     time.Now().UTC(),
	}
	switch inner := event.InnerEvent.Data.(type) {
	case *slackevents.AppMentionEvent:
		base.ChannelID, base.MessageTS, base.ThreadTS = inner.Channel, inner.TimeStamp, inner.ThreadTimeStamp
		base.UserID, base.BotID, base.Text = inner.User, inner.BotID, inner.Text
		base.Kind, base.IsMention = types.SlackEventMessage, true
		return base, true, nil
	case *slackevents.MessageEvent:
		base.ChannelID, base.MessageTS, base.ThreadTS = inner.Channel, inner.TimeStamp, inner.ThreadTimeStamp
		base.UserID, base.BotID, base.Text, base.Subtype = inner.User, inner.BotID, inner.Text, inner.SubType
		base.Kind = types.SlackEventMessage
		switch inner.SubType {
		case "message_changed":
			base.Kind = types.SlackEventEdit
			if inner.Message != nil {
				base.TargetTS, base.MessageTS, base.ThreadTS = inner.Message.Timestamp, inner.Message.Timestamp, inner.Message.ThreadTimestamp
				base.UserID, base.BotID, base.Text = inner.Message.User, inner.Message.BotID, inner.Message.Text
			}
		case "message_deleted":
			base.Kind, base.TargetTS, base.MessageTS, base.Text = types.SlackEventDelete, inner.DeletedTimeStamp, inner.DeletedTimeStamp, ""
		}
		base.IsMention = botUserID != "" && strings.Contains(base.Text, "<@"+botUserID+">")
		if base.EventTime.IsZero() {
			base.EventTime = slackTimestamp(base.MessageTS)
		}
		return base, true, nil
	default:
		return types.SlackEnvelope{}, false, nil
	}
}

func (d *LiveDelivery) Send(ctx context.Context, request types.SlackDeliveryRequest) (types.SlackDeliveryResult, error) {
	if request.Destination.TeamID != d.teamID {
		return types.SlackDeliveryResult{}, errors.New("Slack destination team does not match the configured installation")
	}
	payloads, err := d.renderer.Render(request.Result)
	if err != nil {
		return types.SlackDeliveryResult{}, err
	}
	existing, err := d.deliveryParts(ctx, request)
	if err != nil {
		return types.SlackDeliveryResult{}, fmt.Errorf("reconcile Slack delivery: %w", err)
	}
	var firstTimestamp string
	for _, timestamp := range existing {
		if firstTimestamp == "" || timestamp < firstTimestamp {
			firstTimestamp = timestamp
		}
	}
	for index, payload := range payloads {
		if _, alreadyDelivered := existing[index+1]; alreadyDelivered {
			continue
		}
		encoded, err := json.Marshal(payload.Blocks)
		if err != nil {
			return types.SlackDeliveryResult{}, err
		}
		var blocks slackapi.Blocks
		if err := json.Unmarshal(encoded, &blocks); err != nil {
			return types.SlackDeliveryResult{}, fmt.Errorf("convert rendered Slack blocks: %w", err)
		}
		options := []slackapi.MsgOption{
			slackapi.MsgOptionText(payload.Text, false),
			slackapi.MsgOptionBlocks(blocks.BlockSet...),
			slackapi.MsgOptionMetadata(slackapi.SlackMetadata{EventType: "tos_tag_delivery", EventPayload: map[string]any{"delivery_id": string(request.ID), "part": index + 1}}),
		}
		if request.Destination.ThreadTS != "" {
			options = append(options, slackapi.MsgOptionTS(request.Destination.ThreadTS))
		}
		_, timestamp, err := d.api.PostMessageContext(ctx, request.Destination.ChannelID, options...)
		if err != nil {
			return types.SlackDeliveryResult{}, err
		}
		if firstTimestamp == "" {
			firstTimestamp = timestamp
		}
	}
	return types.SlackDeliveryResult{MessageTS: firstTimestamp, DeliveredAt: time.Now().UTC(), Duplicate: len(existing) == len(payloads)}, nil
}

// deliveryParts finds previously accepted parts by immutable Slack metadata.
// Send calls this before every post, so a worker crash after Slack acceptance
// but before Mongo completion resumes at the first missing part.
func (d *LiveDelivery) deliveryParts(ctx context.Context, request types.SlackDeliveryRequest) (map[int]string, error) {
	found := make(map[int]string)
	cursor := ""
	for page := 0; page < 20; page++ {
		var messages []slackapi.Message
		var hasMore bool
		var next string
		if request.Destination.ThreadTS != "" {
			var err error
			messages, hasMore, next, err = d.api.GetConversationRepliesContext(ctx, &slackapi.GetConversationRepliesParameters{ChannelID: request.Destination.ChannelID, Timestamp: request.Destination.ThreadTS, Cursor: cursor, Limit: 100, IncludeAllMetadata: true})
			if err != nil {
				return nil, err
			}
		} else {
			response, err := d.api.GetConversationHistoryContext(ctx, &slackapi.GetConversationHistoryParameters{ChannelID: request.Destination.ChannelID, Cursor: cursor, Limit: 100, IncludeAllMetadata: true})
			if err != nil {
				return nil, err
			}
			messages, hasMore, next = response.Messages, response.HasMore, response.ResponseMetaData.NextCursor
		}
		for _, message := range messages {
			if message.Metadata.EventType != "tos_tag_delivery" || fmt.Sprint(message.Metadata.EventPayload["delivery_id"]) != string(request.ID) {
				continue
			}
			part, partErr := strconv.Atoi(fmt.Sprint(message.Metadata.EventPayload["part"]))
			if partErr == nil && part > 0 {
				found[part] = message.Timestamp
			}
		}
		if !hasMore || next == "" {
			break
		}
		cursor = next
	}
	return found, nil
}

func slackTimestamp(value string) time.Time {
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return time.Time{}
	}
	whole := int64(seconds)
	return time.Unix(whole, int64((seconds-float64(whole))*float64(time.Second))).UTC()
}
