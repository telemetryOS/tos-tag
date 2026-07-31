package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RobertWHurst/blackbox"
	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"
	"github.com/slack-go/slack/socketmode"

	"github.com/telemetryos/tos-tag/core/deliveries"
	"github.com/telemetryos/tos-tag/types"
)

func TestNormalizeSlackMessageEditDeleteAndMention(t *testing.T) {
	callback := slackevents.EventsAPICallbackEvent{TeamID: "team", EventID: "event", EventTime: 100}
	event := slackevents.EventsAPIEvent{Type: slackevents.CallbackEvent, Data: callback, InnerEvent: slackevents.EventsAPIInnerEvent{Data: &slackevents.MessageEvent{Channel: "channel", TimeStamp: "100.1", User: "user", Text: "hello <@bot>", Message: &slackapi.Msg{Timestamp: "100.1", Text: "hello <@bot>", User: "user"}}}}
	envelope, eligible, err := NormalizeEventsAPI("org", "bot", "envelope", event)
	if err != nil || !eligible {
		t.Fatalf("eligible=%v err=%v", eligible, err)
	}
	if !envelope.IsMention || envelope.Kind != types.SlackEventMessage || envelope.EventID != "message/team/channel/100.1" {
		t.Fatalf("normalized message = %#v", envelope)
	}

	event.InnerEvent.Data = &slackevents.MessageEvent{Channel: "channel", SubType: "message_changed", Message: &slackapi.Msg{Timestamp: "100.1", ThreadTimestamp: "100.0", Text: "edited", User: "user"}}
	envelope, eligible, err = NormalizeEventsAPI("org", "bot", "envelope-2", event)
	if err != nil || !eligible || envelope.Kind != types.SlackEventEdit || envelope.TargetTS != "100.1" || envelope.Text != "edited" {
		t.Fatalf("normalized edit = %#v, eligible=%v err=%v", envelope, eligible, err)
	}

	event.InnerEvent.Data = &slackevents.MessageEvent{Channel: "channel", SubType: "message_deleted", DeletedTimeStamp: "100.1"}
	envelope, eligible, err = NormalizeEventsAPI("org", "bot", "envelope-3", event)
	if err != nil || !eligible || envelope.Kind != types.SlackEventDelete || envelope.TargetTS != "100.1" {
		t.Fatalf("normalized delete = %#v, eligible=%v err=%v", envelope, eligible, err)
	}

	event.InnerEvent.Data = &slackevents.MessageEvent{Channel: "private-channel", ChannelType: slackevents.ChannelTypeGroup, TimeStamp: "100.2", User: "user", Text: "private context"}
	envelope, eligible, err = NormalizeEventsAPI("org", "bot", "envelope-4", event)
	if err != nil || !eligible || !envelope.Restricted {
		t.Fatalf("private message was not marked restricted: %#v, eligible=%v err=%v", envelope, eligible, err)
	}
}

func TestNormalizeSlackParserCallbackPointer(t *testing.T) {
	raw := json.RawMessage(`{"team_id":"team","api_app_id":"app","type":"event_callback","event_id":"event-real","event_time":100,"event":{"type":"message","channel":"channel","user":"user","text":"from Slack","ts":"100.1","event_ts":"100.1"}}`)
	event, err := slackevents.ParseEvent(raw, slackevents.OptionNoVerifyToken())
	if err != nil {
		t.Fatal(err)
	}
	envelope, eligible, err := NormalizeEventsAPI("org", "bot", "envelope-real", event)
	if err != nil || !eligible {
		t.Fatalf("eligible=%v err=%v", eligible, err)
	}
	if envelope.EventID != "message/team/channel/100.1" || envelope.TeamID != "team" || envelope.ChannelID != "channel" || envelope.Text != "from Slack" {
		t.Fatalf("normalized parsed message = %#v", envelope)
	}
}

func TestNormalizeAppMentionAndMessageShareCanonicalEventID(t *testing.T) {
	callback := slackevents.EventsAPICallbackEvent{TeamID: "team", EventID: "mention-event", EventTime: 100}
	mentionEvent := slackevents.EventsAPIEvent{Type: slackevents.CallbackEvent, Data: callback, InnerEvent: slackevents.EventsAPIInnerEvent{Data: &slackevents.AppMentionEvent{Channel: "channel", TimeStamp: "100.1", User: "user", Text: "hello <@bot>"}}}
	mention, eligible, err := NormalizeEventsAPI("org", "bot", "mention-envelope", mentionEvent)
	if err != nil || !eligible {
		t.Fatalf("mention eligible=%v err=%v", eligible, err)
	}

	callback.EventID = "message-event"
	messageEvent := slackevents.EventsAPIEvent{Type: slackevents.CallbackEvent, Data: callback, InnerEvent: slackevents.EventsAPIInnerEvent{Data: &slackevents.MessageEvent{Channel: "channel", TimeStamp: "100.1", User: "user", Text: "hello <@bot>"}}}
	message, eligible, err := NormalizeEventsAPI("org", "bot", "message-envelope", messageEvent)
	if err != nil || !eligible {
		t.Fatalf("message eligible=%v err=%v", eligible, err)
	}
	if mention.EventID != message.EventID || mention.EventID != "message/team/channel/100.1" {
		t.Fatalf("canonical IDs differ: mention=%q message=%q", mention.EventID, message.EventID)
	}
}

func TestLiveIngressAcknowledgesDurableRetryDuplicateAfterReconnect(t *testing.T) {
	transport := newFakeSocketModeTransport()
	ingress := &LiveIngress{
		options: LiveOptions{OrganizationID: "org", AppID: "app", TeamID: "team", BotUserID: "bot", Logger: blackbox.New()},
		client:  transport,
	}
	var mu sync.Mutex
	calls := 0
	handler := func(_ context.Context, envelope types.SlackEnvelope) (AcceptResult, error) {
		mu.Lock()
		defer mu.Unlock()
		calls++
		transport.handled <- struct{}{}
		if envelope.EventID != "message/team/channel/100.1" {
			t.Fatalf("unexpected envelope: %#v", envelope)
		}
		if calls == 1 {
			return AcceptResult{}, errors.New("persistence unavailable")
		}
		return AcceptResult{Duplicate: true}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := ingress.Start(ctx, handler); err != nil {
		t.Fatal(err)
	}

	transport.events <- socketmode.Event{Type: socketmode.EventTypeConnected, Data: &socketmode.ConnectedEvent{ConnectionCount: 1}}
	transport.events <- socketmode.Event{Type: socketmode.EventTypeHello, Request: &socketmode.Request{ConnectionInfo: socketmode.ConnectionInfo{AppID: "app"}}}
	first := retryEventsAPIEvent("envelope-first", 0, "")
	transport.events <- first
	select {
	case <-transport.handled:
	case <-time.After(time.Second):
		t.Fatal("first delivery was not handled")
	}
	select {
	case envelopeID := <-transport.acked:
		t.Fatalf("failed first delivery was acknowledged: %s", envelopeID)
	default:
	}

	retry := retryEventsAPIEvent("envelope-retry", 1, "timeout")
	transport.events <- retry
	select {
	case envelopeID := <-transport.acked:
		if envelopeID != "envelope-retry" {
			t.Fatalf("acked envelope %q", envelopeID)
		}
	case <-time.After(time.Second):
		t.Fatal("durable retry duplicate was not acknowledged")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("handler calls = %d", calls)
	}
	if err := ingress.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func retryEventsAPIEvent(envelopeID string, retryAttempt int, retryReason string) socketmode.Event {
	callback := &slackevents.EventsAPICallbackEvent{TeamID: "team", EventID: "event-retry", EventTime: 100}
	return socketmode.Event{
		Type: socketmode.EventTypeEventsAPI,
		Data: slackevents.EventsAPIEvent{
			Type: slackevents.CallbackEvent,
			Data: callback,
			InnerEvent: slackevents.EventsAPIInnerEvent{Data: &slackevents.MessageEvent{
				Channel: "channel", TimeStamp: "100.1", User: "user", Text: "retry",
			}},
		},
		Request: &socketmode.Request{EnvelopeID: envelopeID, RetryAttempt: retryAttempt, RetryReason: retryReason},
	}
}

type fakeSocketModeTransport struct {
	events  chan socketmode.Event
	acked   chan string
	handled chan struct{}
}

func newFakeSocketModeTransport() *fakeSocketModeTransport {
	return &fakeSocketModeTransport{events: make(chan socketmode.Event, 8), acked: make(chan string, 8), handled: make(chan struct{}, 8)}
}

func (f *fakeSocketModeTransport) RunContext(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeSocketModeTransport) AckCtx(_ context.Context, envelopeID string, _ any) error {
	f.acked <- envelopeID
	return nil
}

func (f *fakeSocketModeTransport) EventsChannel() <-chan socketmode.Event {
	return f.events
}

func TestSlackEnvelopeLogContextExcludesContent(t *testing.T) {
	context := slackEnvelopeLogContext(types.SlackEnvelope{
		OrganizationID: "org", EventID: "event", ChannelID: "channel", Text: "private-message-content",
	})
	encoded := fmt.Sprint(context)
	if strings.Contains(encoded, "private-message-content") || context["text_bytes"] != len("private-message-content") {
		t.Fatalf("Slack log context leaked content or omitted its bounded size: %s", encoded)
	}
}

func TestLiveDeliveryFixesDestinationAndUsesRenderer(t *testing.T) {
	fake := &fakePostMessage{}
	delivery := &LiveDelivery{teamID: "team", api: fake, renderer: deliveries.NewRenderer()}
	request := types.SlackDeliveryRequest{ID: "delivery-1", Destination: types.SlackDestination{TeamID: "team", ChannelID: "channel", ThreadTS: "100.0"}, Result: types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentMRKDWN, Text: "*Ready* for `ENG-1`"}}}}
	result, err := delivery.Send(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if fake.channel != "channel" || fake.calls != 1 || result.MessageTS != "200.1" {
		t.Fatalf("fake=%#v result=%#v", fake, result)
	}
	request.Destination.TeamID = "forged-team"
	if _, err := delivery.Send(context.Background(), request); err == nil {
		t.Fatal("forged team destination was accepted")
	}
}

func TestLiveDeliveryAppliesClassifierReactionToSourceMessage(t *testing.T) {
	fake := &fakePostMessage{}
	delivery := &LiveDelivery{teamID: "team", api: fake, renderer: deliveries.NewRenderer()}
	request := types.SlackReactionRequest{IdempotencyKey: "decision/1/reaction", TeamID: "team", ChannelID: "channel", MessageTS: "100.1", Emoji: "eyes"}
	result, err := delivery.React(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.AppliedAt.IsZero() || len(fake.reactions) != 1 || fake.reactions[0].Channel != "channel" || fake.reactions[0].Timestamp != "100.1" {
		t.Fatalf("reaction result=%#v items=%#v", result, fake.reactions)
	}
	request.TeamID = "forged-team"
	if _, err := delivery.React(context.Background(), request); err == nil {
		t.Fatal("forged reaction team was accepted")
	}
}

func TestSlackBlocksFromRenderedPreservesOptionalTableFields(t *testing.T) {
	payloads, err := deliveries.NewRenderer().Render(types.SlackResult{Segments: []types.SlackSegment{{
		Kind: types.SlackSegmentTable,
		Table: &types.SlackTable{
			Columns: []types.SlackTableColumn{{Header: "Check"}, {Header: "Result"}},
			Rows:    [][]types.SlackTableCell{{{Type: "raw_text", Text: "render"}, {Type: "raw_text", Text: "pass"}}},
		},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := slackBlocksFromRendered(payloads[0].Blocks)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"align":""`) {
		t.Fatalf("Slack block conversion introduced an invalid empty alignment: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"column_settings":[{"is_wrapped":false},{"is_wrapped":false}]`) {
		t.Fatalf("Slack block conversion changed renderer output: %s", encoded)
	}
}

type fakePostMessage struct {
	channel   string
	calls     int
	messages  []slackapi.Message
	reactions []slackapi.ItemRef
}

func (f *fakePostMessage) AddReactionContext(_ context.Context, _ string, item slackapi.ItemRef) error {
	f.reactions = append(f.reactions, item)
	return nil
}

func (f *fakePostMessage) PostMessageContext(_ context.Context, channel string, _ ...slackapi.MsgOption) (string, string, error) {
	f.channel, f.calls = channel, f.calls+1
	return channel, "200.1", nil
}

func (f *fakePostMessage) GetConversationHistoryContext(_ context.Context, _ *slackapi.GetConversationHistoryParameters) (*slackapi.GetConversationHistoryResponse, error) {
	return &slackapi.GetConversationHistoryResponse{Messages: f.messages}, nil
}

func (f *fakePostMessage) GetConversationRepliesContext(_ context.Context, _ *slackapi.GetConversationRepliesParameters) ([]slackapi.Message, bool, string, error) {
	return f.messages, false, "", nil
}

func TestLiveDeliveryReconcilesAcceptedPartBeforeRetry(t *testing.T) {
	fake := &fakePostMessage{messages: []slackapi.Message{{Msg: slackapi.Msg{Timestamp: "199.9", Metadata: slackapi.SlackMetadata{EventType: "tos_tag_delivery", EventPayload: map[string]any{"delivery_id": "delivery-1", "part": 1}}}}}}
	delivery := &LiveDelivery{teamID: "team", api: fake, renderer: deliveries.NewRenderer()}
	request := types.SlackDeliveryRequest{ID: "delivery-1", Destination: types.SlackDestination{TeamID: "team", ChannelID: "channel"}, Result: types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentMRKDWN, Text: "already accepted"}}}}
	result, err := delivery.Send(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if fake.calls != 0 || !result.Duplicate || result.MessageTS != "199.9" {
		t.Fatalf("calls=%d result=%#v", fake.calls, result)
	}
}
