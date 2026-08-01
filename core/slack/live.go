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

	"github.com/RobertWHurst/blackbox"
	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/telemetryos/tos-tag/core/deliveries"
	"github.com/telemetryos/tos-tag/types"
)

type LiveOptions struct {
	OrganizationID    string
	AppID             string
	TeamID            string
	AppLevelToken     string
	BotUserOAuthToken string
	BotUserID         string
	Logger            *blackbox.Logger
}

type LiveIngress struct {
	options LiveOptions
	client  socketModeTransport

	mu                 sync.Mutex
	cancel             context.CancelFunc
	done               chan struct{}
	started            bool
	interactionHandler ApprovalInteractionHandler
}

func (s *LiveIngress) SetApprovalInteractionHandler(handler ApprovalInteractionHandler) {
	s.mu.Lock()
	s.interactionHandler = handler
	s.mu.Unlock()
}

type socketModeTransport interface {
	RunContext(context.Context) error
	AckCtx(context.Context, string, any) error
	EventsChannel() <-chan socketmode.Event
}

type managedSocketModeTransport struct {
	client *socketmode.Client
}

func (t managedSocketModeTransport) RunContext(ctx context.Context) error {
	return t.client.RunContext(ctx)
}

func (t managedSocketModeTransport) AckCtx(ctx context.Context, envelopeID string, payload any) error {
	return t.client.AckCtx(ctx, envelopeID, payload)
}

func (t managedSocketModeTransport) EventsChannel() <-chan socketmode.Event {
	return t.client.Events
}

type deliveryAPI interface {
	PostMessageContext(context.Context, string, ...slackapi.MsgOption) (string, string, error)
	UpdateMessageContext(context.Context, string, string, ...slackapi.MsgOption) (string, string, string, error)
	AddReactionContext(context.Context, string, slackapi.ItemRef) error
	GetConversationHistoryContext(context.Context, *slackapi.GetConversationHistoryParameters) (*slackapi.GetConversationHistoryResponse, error)
	GetConversationRepliesContext(context.Context, *slackapi.GetConversationRepliesParameters) ([]slackapi.Message, bool, string, error)
}

type LiveDelivery struct {
	teamID   string
	api      deliveryAPI
	renderer *deliveries.Renderer
	logger   *blackbox.Logger
}

// NewLive constructs the production adapters without opening a connection.
// Start is the only method that initiates Socket Mode network activity.
func NewLive(options LiveOptions, renderer *deliveries.Renderer) (*LiveIngress, *LiveDelivery, error) {
	if options.OrganizationID == "" || !strings.HasPrefix(options.AppID, "A") || options.TeamID == "" || !strings.HasPrefix(options.AppLevelToken, "xapp-") || !strings.HasPrefix(options.BotUserOAuthToken, "xoxb-") {
		return nil, nil, errors.New("invalid live Slack options")
	}
	if renderer == nil {
		return nil, nil, errors.New("Slack renderer is required")
	}
	api := slackapi.New(options.BotUserOAuthToken, slackapi.OptionAppLevelToken(options.AppLevelToken))
	client := managedSocketModeTransport{client: socketmode.New(api)}
	if options.Logger == nil {
		options.Logger = blackbox.New()
	}
	options.Logger = options.Logger.WithCtx(blackbox.Ctx{"component": "slack", "slack_app_id": options.AppID, "slack_team_id": options.TeamID})
	return &LiveIngress{options: options, client: client}, &LiveDelivery{teamID: options.TeamID, api: api, renderer: renderer, logger: options.Logger}, nil
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
	s.options.Logger.Info("Slack Socket Mode loop started")
	defer s.options.Logger.Info("Slack Socket Mode loop stopped")
	runDone := make(chan error, 1)
	go func() { runDone <- s.client.RunContext(ctx) }()
	for {
		select {
		case <-ctx.Done():
			<-runDone
			return
		case err := <-runDone:
			if ctx.Err() == nil {
				s.options.Logger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", err)}).Error("Slack Socket Mode client exited unexpectedly")
			}
			return
		case event, open := <-s.client.EventsChannel():
			if !open {
				s.options.Logger.Warn("Slack Socket Mode event stream closed")
				return
			}
			requestContext := blackbox.Ctx{"socket_event_type": string(event.Type)}
			if event.Request != nil {
				requestContext["envelope_id"] = event.Request.EnvelopeID
				requestContext["retry_attempt"] = event.Request.RetryAttempt
				requestContext["retry_reason"] = event.Request.RetryReason
			}
			eventLogger := s.options.Logger.WithCtx(requestContext)
			switch event.Type {
			case socketmode.EventTypeConnecting:
				eventLogger.Info("Slack Socket Mode connecting")
				continue
			case socketmode.EventTypeConnected:
				connectionCount := 0
				if connected, ok := event.Data.(*socketmode.ConnectedEvent); ok {
					connectionCount = connected.ConnectionCount
				}
				eventLogger.WithCtx(blackbox.Ctx{"connection_count": connectionCount, "reconnected": connectionCount > 0}).Info("Slack Socket Mode transport connected")
				continue
			case socketmode.EventTypeHello:
				if event.Request == nil {
					eventLogger.Error("Slack Socket Mode hello missing request metadata")
					continue
				}
				actualAppID := event.Request.ConnectionInfo.AppID
				if actualAppID != s.options.AppID {
					eventLogger.WithCtx(blackbox.Ctx{"actual_slack_app_id": actualAppID}).Error("Slack App-Level Token belongs to a different app")
					s.cancel()
					continue
				}
				eventLogger.Info("Slack Socket Mode hello verified")
				continue
			case socketmode.EventTypeInvalidAuth, socketmode.EventTypeConnectionError, socketmode.EventTypeIncomingError, socketmode.EventTypeErrorWriteFailed, socketmode.EventTypeErrorBadMessage:
				eventLogger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", event.Data)}).Error("Slack Socket Mode transport error")
				continue
			case socketmode.EventTypeEventsAPI:
				eventLogger.Info("Slack Events API envelope received")
			case socketmode.EventTypeInteractive:
				if event.Request == nil {
					eventLogger.Error("Slack interaction missing request metadata")
					continue
				}
				interaction, eligible, normalizeErr := NormalizeApprovalInteraction(s.options, event.Data)
				if normalizeErr != nil {
					eventLogger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", normalizeErr)}).Error("Slack approval interaction rejected")
					continue
				}
				if !eligible {
					eventLogger.Debug("Slack interaction ignored")
					continue
				}
				s.mu.Lock()
				interactionHandler := s.interactionHandler
				s.mu.Unlock()
				if interactionHandler == nil {
					eventLogger.Error("Slack approval interaction has no durable handler")
					continue
				}
				if handleErr := interactionHandler(ctx, interaction); handleErr != nil {
					eventLogger.WithCtx(blackbox.Ctx{"approval_id": interaction.ApprovalID, "approver_id": interaction.UserID, "error_type": fmt.Sprintf("%T", handleErr)}).Error("Slack approval interaction persistence failed; acknowledgement withheld")
					continue
				}
				if ackErr := s.client.AckCtx(ctx, event.Request.EnvelopeID, nil); ackErr != nil {
					eventLogger.WithCtx(blackbox.Ctx{"approval_id": interaction.ApprovalID, "error_type": fmt.Sprintf("%T", ackErr)}).Error("Slack approval interaction acknowledgement failed")
					continue
				}
				eventLogger.WithCtx(blackbox.Ctx{"approval_id": interaction.ApprovalID, "approver_id": interaction.UserID, "approved": interaction.Approve}).Info("Slack approval interaction durably handled and acknowledged")
				continue
			default:
				eventLogger.Debug("Slack Socket Mode event ignored by Events API ingress")
				continue
			}
			if event.Request == nil {
				eventLogger.Error("Slack Events API envelope missing request metadata")
				continue
			}
			started := time.Now()
			normalized, eligible, err := NormalizeEventsAPI(s.options.OrganizationID, s.options.BotUserID, event.Request.EnvelopeID, event.Data)
			if err != nil {
				eventLogger.WithCtx(blackbox.Ctx{"duration_ms": time.Since(started).Milliseconds(), "error_type": fmt.Sprintf("%T", err)}).Error("Slack Events API normalization failed")
				continue
			}
			if !eligible {
				if ackErr := s.client.AckCtx(ctx, event.Request.EnvelopeID, nil); ackErr != nil {
					eventLogger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", ackErr)}).Error("Slack ineligible envelope acknowledgement failed")
				} else {
					eventLogger.WithCtx(blackbox.Ctx{"duration_ms": time.Since(started).Milliseconds()}).Info("Slack ineligible envelope acknowledged")
				}
				continue
			}
			envelopeLogger := eventLogger.WithCtx(slackEnvelopeLogContext(normalized))
			envelopeLogger.Info("Slack event normalized")
			accepted, err := handler(ctx, normalized)
			if err != nil {
				// Deliberately do not acknowledge: Slack may retry after a
				// persistence or admission failure.
				envelopeLogger.WithCtx(blackbox.Ctx{"duration_ms": time.Since(started).Milliseconds(), "error_type": fmt.Sprintf("%T", err)}).Error("Slack event rejected; acknowledgement withheld")
				continue
			}
			if ackErr := s.client.AckCtx(ctx, event.Request.EnvelopeID, nil); ackErr != nil {
				envelopeLogger.WithCtx(blackbox.Ctx{"duration_ms": time.Since(started).Milliseconds(), "duplicate": accepted.Duplicate, "ignored": accepted.Ignored, "error_type": fmt.Sprintf("%T", ackErr)}).Error("Slack event acknowledgement failed")
				continue
			}
			if accepted.Ignored {
				envelopeLogger.WithCtx(blackbox.Ctx{"duration_ms": time.Since(started).Milliseconds(), "ignored": true}).Info("Slack policy-excluded envelope acknowledged without persistence")
				continue
			}
			envelopeLogger.WithCtx(blackbox.Ctx{"duration_ms": time.Since(started).Milliseconds(), "duplicate": accepted.Duplicate, "ignored": false}).Info("Slack event durably accepted and acknowledged")
		}
	}
}

func NormalizeApprovalInteraction(options LiveOptions, data any) (ApprovalInteraction, bool, error) {
	var callback slackapi.InteractionCallback
	switch value := data.(type) {
	case slackapi.InteractionCallback:
		callback = value
	case *slackapi.InteractionCallback:
		if value == nil {
			return ApprovalInteraction{}, false, errors.New("Slack interaction payload is nil")
		}
		callback = *value
	default:
		return ApprovalInteraction{}, false, nil
	}
	if callback.Type != slackapi.InteractionTypeBlockActions {
		return ApprovalInteraction{}, false, nil
	}
	if callback.APIAppID != "" && callback.APIAppID != options.AppID {
		return ApprovalInteraction{}, false, errors.New("Slack interaction app does not match installation")
	}
	if callback.Team.ID != options.TeamID || callback.User.ID == "" || len(callback.ActionCallback.BlockActions) != 1 {
		return ApprovalInteraction{}, false, errors.New("Slack interaction scope is invalid")
	}
	action := callback.ActionCallback.BlockActions[0]
	if action == nil || action.Value == "" {
		return ApprovalInteraction{}, false, errors.New("Slack interaction action is invalid")
	}
	approve := false
	switch action.ActionID {
	case "tos_tag_approval_approve":
		approve = true
	case "tos_tag_approval_deny":
	default:
		return ApprovalInteraction{}, false, nil
	}
	channelID := callback.Channel.ID
	if channelID == "" {
		channelID = callback.Container.ChannelID
	}
	if channelID == "" {
		return ApprovalInteraction{}, false, errors.New("Slack interaction channel is missing")
	}
	messageTS := callback.Container.MessageTs
	if messageTS == "" {
		messageTS = callback.Message.Timestamp
	}
	if messageTS == "" {
		messageTS = callback.MessageTs
	}
	if messageTS == "" {
		return ApprovalInteraction{}, false, errors.New("Slack interaction message is missing")
	}
	return ApprovalInteraction{OrganizationID: options.OrganizationID, WorkspaceID: callback.Team.ID, ChannelID: channelID, UserID: callback.User.ID, ApprovalID: action.Value, MessageTS: messageTS, Approve: approve}, true, nil
}

func slackEnvelopeLogContext(envelope types.SlackEnvelope) blackbox.Ctx {
	return blackbox.Ctx{
		"organization_id": envelope.OrganizationID,
		"event_id":        envelope.EventID,
		"channel_id":      envelope.ChannelID,
		"message_ts":      envelope.MessageTS,
		"thread_ts":       envelope.ThreadTS,
		"event_kind":      string(envelope.Kind),
		"event_subtype":   envelope.Subtype,
		"is_mention":      envelope.IsMention,
		"is_bot_event":    envelope.BotID != "",
		"text_bytes":      len(envelope.Text),
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
	var callback *slackevents.EventsAPICallbackEvent
	switch value := event.Data.(type) {
	case *slackevents.EventsAPICallbackEvent:
		callback = value
	case slackevents.EventsAPICallbackEvent:
		callback = &value
	}
	if callback == nil {
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
		base.Kind, base.IsMention, base.OriginTag = types.SlackEventMessage, true, "slack_app_mention"
		base.EventID = canonicalMessageEventID(base.EventID, base.TeamID, base.ChannelID, base.MessageTS)
		return base, true, nil
	case *slackevents.MessageEvent:
		base.ChannelID, base.MessageTS, base.ThreadTS = inner.Channel, inner.TimeStamp, inner.ThreadTimeStamp
		base.UserID, base.BotID, base.Text, base.Subtype = inner.User, inner.BotID, inner.Text, inner.SubType
		base.Restricted = inner.ChannelType == slackevents.ChannelTypeGroup || inner.ChannelType == slackevents.ChannelTypeIM || inner.ChannelType == slackevents.ChannelTypeMPIM
		base.Kind, base.OriginTag = types.SlackEventMessage, "slack_message"
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
		if base.Kind == types.SlackEventMessage {
			base.EventID = canonicalMessageEventID(base.EventID, base.TeamID, base.ChannelID, base.MessageTS)
		}
		if base.EventTime.IsZero() {
			base.EventTime = slackTimestamp(base.MessageTS)
		}
		return base, true, nil
	default:
		return types.SlackEnvelope{}, false, nil
	}
}

// canonicalMessageEventID coalesces Slack's app_mention and message callbacks
// for the same message while preserving Slack's callback ID as a fallback.
// Mutation callbacks keep their own callback IDs so every edit/delete remains
// independently observable.
func canonicalMessageEventID(fallback, teamID, channelID, messageTS string) string {
	if teamID == "" || channelID == "" || messageTS == "" {
		return fallback
	}
	return "message/" + teamID + "/" + channelID + "/" + messageTS
}

func (d *LiveDelivery) Send(ctx context.Context, request types.SlackDeliveryRequest) (types.SlackDeliveryResult, error) {
	started := time.Now()
	logger := d.logger
	if logger == nil {
		logger = blackbox.New()
	}
	requestLogger := logger.WithCtx(blackbox.Ctx{
		"delivery_id": request.ID, "channel_id": request.Destination.ChannelID,
		"thread_ts": request.Destination.ThreadTS, "segment_count": len(request.Result.Segments),
	})
	requestLogger.Info("Slack delivery requested")
	if request.Destination.TeamID != d.teamID {
		requestLogger.Warn("Slack delivery denied for mismatched workspace")
		return types.SlackDeliveryResult{}, errors.New("Slack destination team does not match the configured installation")
	}
	payloads, err := d.renderer.Render(request.Result)
	if err != nil {
		requestLogger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", err)}).Error("Slack delivery rendering failed")
		return types.SlackDeliveryResult{}, err
	}
	if request.Destination.UpdateTS != "" && len(payloads) != 1 {
		return types.SlackDeliveryResult{}, errors.New("Slack message updates must render as exactly one payload")
	}
	existing, err := d.deliveryParts(ctx, request)
	if err != nil {
		requestLogger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", err)}).Error("Slack delivery reconciliation failed")
		return types.SlackDeliveryResult{}, fmt.Errorf("reconcile Slack delivery: %w", err)
	}
	requestLogger.WithCtx(blackbox.Ctx{"payload_count": len(payloads), "reconciled_part_count": len(existing)}).Info("Slack delivery reconciliation completed")
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
		blocks, err := slackBlocksFromRendered(payload.Blocks)
		if err != nil {
			return types.SlackDeliveryResult{}, err
		}
		options := []slackapi.MsgOption{
			slackapi.MsgOptionText(payload.Text, false),
			slackapi.MsgOptionBlocks(blocks...),
			slackapi.MsgOptionMetadata(slackapi.SlackMetadata{EventType: "tos_tag_delivery", EventPayload: map[string]any{"delivery_id": string(request.ID), "part": index + 1}}),
		}
		if request.Destination.ThreadTS != "" && request.Destination.UpdateTS == "" {
			options = append(options, slackapi.MsgOptionTS(request.Destination.ThreadTS))
		}
		var timestamp string
		if request.Destination.UpdateTS != "" {
			_, timestamp, _, err = d.api.UpdateMessageContext(ctx, request.Destination.ChannelID, request.Destination.UpdateTS, options...)
			if timestamp == "" {
				timestamp = request.Destination.UpdateTS
			}
		} else {
			_, timestamp, err = d.api.PostMessageContext(ctx, request.Destination.ChannelID, options...)
		}
		if err != nil {
			failureContext := blackbox.Ctx{"part": index + 1, "error_type": fmt.Sprintf("%T", err)}
			if code := slackAPIErrorCode(err); code != "" {
				failureContext["slack_error_code"] = code
			}
			requestLogger.WithCtx(failureContext).Error("Slack delivery part failed")
			return types.SlackDeliveryResult{}, err
		}
		requestLogger.WithCtx(blackbox.Ctx{"part": index + 1, "message_ts": timestamp}).Info("Slack delivery part accepted")
		if firstTimestamp == "" {
			firstTimestamp = timestamp
		}
	}
	result := types.SlackDeliveryResult{MessageTS: firstTimestamp, DeliveredAt: time.Now().UTC(), Duplicate: len(existing) == len(payloads)}
	requestLogger.WithCtx(blackbox.Ctx{"duration_ms": time.Since(started).Milliseconds(), "duplicate": result.Duplicate, "message_ts": result.MessageTS}).Info("Slack delivery completed")
	return result, nil
}

func (d *LiveDelivery) React(ctx context.Context, request types.SlackReactionRequest) (types.SlackReactionResult, error) {
	started := time.Now()
	logger := d.logger
	if logger == nil {
		logger = blackbox.New()
	}
	requestLogger := logger.WithCtx(blackbox.Ctx{
		"reaction_idempotency_key": request.IdempotencyKey,
		"channel_id":               request.ChannelID,
		"message_ts":               request.MessageTS,
		"emoji":                    request.Emoji,
	})
	requestLogger.Info("Slack reaction requested")
	if request.TeamID != d.teamID {
		requestLogger.Warn("Slack reaction denied for mismatched workspace")
		return types.SlackReactionResult{}, errors.New("Slack reaction team does not match the configured installation")
	}
	if request.ChannelID == "" || request.MessageTS == "" || !validReactionName(request.Emoji) {
		return types.SlackReactionResult{}, errors.New("invalid Slack reaction request")
	}
	err := d.api.AddReactionContext(ctx, request.Emoji, slackapi.ItemRef{Channel: request.ChannelID, Timestamp: request.MessageTS})
	duplicate := false
	if err != nil {
		var slackError slackapi.SlackErrorResponse
		if errors.As(err, &slackError) && slackError.Err == "already_reacted" {
			duplicate = true
		} else {
			failureContext := blackbox.Ctx{"duration_ms": time.Since(started).Milliseconds(), "error_type": fmt.Sprintf("%T", err)}
			if code := slackAPIErrorCode(err); code != "" {
				failureContext["slack_error_code"] = code
			}
			requestLogger.WithCtx(failureContext).Error("Slack reaction failed")
			return types.SlackReactionResult{}, err
		}
	}
	result := types.SlackReactionResult{AppliedAt: time.Now().UTC(), Duplicate: duplicate}
	requestLogger.WithCtx(blackbox.Ctx{"duration_ms": time.Since(started).Milliseconds(), "duplicate": duplicate}).Info("Slack reaction completed")
	return result, nil
}

func validReactionName(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if (character < 'a' || character > 'z') && (character < '0' || character > '9') && character != '_' && character != '-' && character != '+' {
			return false
		}
	}
	return true
}

// slackBlocksFromRendered preserves the renderer's validated JSON exactly.
// Decoding through slackapi.Blocks and then encoding again can introduce zero
// values such as an empty table-column alignment, which Slack rejects.
func slackBlocksFromRendered(rendered []map[string]any) ([]slackapi.Block, error) {
	blocks := make([]slackapi.Block, 0, len(rendered))
	for index, value := range rendered {
		encoded, err := json.Marshal(value)
		if err != nil {
			return nil, fmt.Errorf("encode rendered Slack block %d: %w", index, err)
		}
		block, err := slackapi.BlockFromJSON(string(encoded))
		if err != nil {
			return nil, fmt.Errorf("convert rendered Slack block %d: %w", index, err)
		}
		blocks = append(blocks, block)
	}
	return blocks, nil
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
