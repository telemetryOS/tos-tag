package slack

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
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

func TestNormalizeSlackAgentMessagesPreservesLoopPreventionSignals(t *testing.T) {
	callback := slackevents.EventsAPICallbackEvent{TeamID: "team", EventID: "event", EventTime: 100}
	for name, message := range map[string]*slackevents.MessageEvent{
		"bot id":            {Channel: "channel", TimeStamp: "100.1", User: "U-claude", BotID: "B-claude", Text: "<@bot> should we continue?"},
		"bot subtype":       {Channel: "channel", TimeStamp: "100.2", User: "U-app", SubType: types.SlackMessageSubtypeBotMessage, Text: "<@bot> should we continue?"},
		"assistant subtype": {Channel: "channel", TimeStamp: "100.3", User: "U-agent", SubType: types.SlackMessageSubtypeAssistantAppThread, Text: "<@bot> should we continue?"},
	} {
		t.Run(name, func(t *testing.T) {
			event := slackevents.EventsAPIEvent{Type: slackevents.CallbackEvent, Data: callback, InnerEvent: slackevents.EventsAPIInnerEvent{Data: message}}
			envelope, eligible, err := NormalizeEventsAPI("org", "bot", "envelope", event)
			if err != nil || !eligible || !envelope.IsMention || !envelope.IntegrationAuthored() {
				t.Fatalf("normalized agent message = %#v eligible=%v err=%v", envelope, eligible, err)
			}
		})
	}
}

func TestSocketModeErrorContextKeepsDiagnosticMessage(t *testing.T) {
	ctx := socketModeErrorContext(errors.New("websocket closed unexpectedly"))
	if ctx["error_type"] != "*errors.errorString" || ctx["error"] != "websocket closed unexpectedly" {
		t.Fatalf("socket error context = %#v", ctx)
	}
	nonError := socketModeErrorContext("opaque")
	if nonError["error_type"] != "string" {
		t.Fatalf("non-error context = %#v", nonError)
	}
	if _, exists := nonError["error"]; exists {
		t.Fatalf("non-error payload fabricated an error message: %#v", nonError)
	}
}

func TestNormalizeBotMembershipChangeOnlyAcceptsConfiguredBot(t *testing.T) {
	callback := slackevents.EventsAPICallbackEvent{TeamID: "team", EventID: "membership-event", EventTime: 100}
	event := slackevents.EventsAPIEvent{Type: slackevents.CallbackEvent, Data: callback, InnerEvent: slackevents.EventsAPIInnerEvent{Data: &slackevents.MemberJoinedChannelEvent{User: "bot", Channel: "channel", Team: "team"}}}
	change, eligible, err := NormalizeBotMembershipChange("org", "bot", event)
	if err != nil || !eligible || !change.Joined || change.OrganizationID != "org" || change.WorkspaceID != "team" || change.ChannelID != "channel" {
		t.Fatalf("joined membership change = %#v eligible=%v err=%v", change, eligible, err)
	}
	event.InnerEvent.Data = &slackevents.MemberLeftChannelEvent{User: "bot", Channel: "channel", Team: "team"}
	change, eligible, err = NormalizeBotMembershipChange("org", "bot", event)
	if err != nil || !eligible || change.Joined {
		t.Fatalf("left membership change = %#v eligible=%v err=%v", change, eligible, err)
	}
	event.InnerEvent.Data = &slackevents.MemberJoinedChannelEvent{User: "someone-else", Channel: "channel", Team: "team"}
	if _, eligible, err = NormalizeBotMembershipChange("org", "bot", event); err != nil || eligible {
		t.Fatalf("non-bot membership event eligible=%v err=%v", eligible, err)
	}
}

func TestApprovalInteractionDiagnosticCodeIsSafeAndActionable(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{nil, "none"},
		{errors.New("independent approver required"), "independent_approver_required"},
		{errors.New("Slack approver is not authorized: denied"), "approver_not_authorized"},
		{errors.New("resume approved job: lease conflict"), "job_resume_failed"},
		{errors.New("database unavailable at secret host"), "persistence_failed"},
	}
	for _, test := range tests {
		if got := approvalInteractionDiagnosticCode(test.err); got != test.want {
			t.Fatalf("approvalInteractionDiagnosticCode(%v) = %q, want %q", test.err, got, test.want)
		}
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

func TestLiveIngressAcknowledgesPolicyExcludedEnvelope(t *testing.T) {
	transport := newFakeSocketModeTransport()
	ingress := &LiveIngress{
		options: LiveOptions{OrganizationID: "org", AppID: "app", TeamID: "team", BotUserID: "bot", Logger: blackbox.New()},
		client:  transport,
	}
	handler := func(_ context.Context, _ types.SlackEnvelope) (AcceptResult, error) {
		transport.handled <- struct{}{}
		return AcceptResult{Ignored: true}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := ingress.Start(ctx, handler); err != nil {
		t.Fatal(err)
	}
	transport.events <- retryEventsAPIEvent("envelope-ignored", 0, "")
	select {
	case envelopeID := <-transport.acked:
		if envelopeID != "envelope-ignored" {
			t.Fatalf("acked envelope %q", envelopeID)
		}
	case <-time.After(time.Second):
		t.Fatal("policy-excluded envelope was not acknowledged")
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

func TestSlackAPIErrorCodeIsSafeAndSpecific(t *testing.T) {
	err := fmt.Errorf("post Slack message: %w", slackapi.SlackErrorResponse{Err: "invalid_blocks"})
	if got := slackAPIErrorCode(err); got != "invalid_blocks" {
		t.Fatalf("error code = %q, want invalid_blocks", got)
	}
	if got := slackAPIErrorCode(errors.New("network unavailable")); got != "" {
		t.Fatalf("non-Slack error code = %q, want empty", got)
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

func TestSlackBlocksFromRenderedPreservesAgentPresentationBlocks(t *testing.T) {
	payloads, err := deliveries.NewRenderer().Render(types.SlackResult{Segments: []types.SlackSegment{
		{Kind: types.SlackSegmentCard, Card: &types.SlackCard{Title: "Deployment", Body: "Healthy"}},
		{Kind: types.SlackSegmentCarousel, Carousel: &types.SlackCarousel{Cards: []types.SlackCard{{Title: "A", Body: "First"}, {Title: "B", Body: "Second"}}}},
		{Kind: types.SlackSegmentTable, Table: &types.SlackTable{Caption: "Latency", PageSize: 5, Columns: []types.SlackTableColumn{{Header: "Service"}, {Header: "P95"}}, Rows: [][]types.SlackTableCell{{{Type: "raw_text", Text: "gateway"}, {Type: "raw_number", Number: 42}}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 1 {
		t.Fatalf("presentation payload count=%d", len(payloads))
	}
	var encoded []byte
	for _, payload := range payloads {
		blocks, conversionErr := slackBlocksFromRendered(payload.Blocks)
		if conversionErr != nil {
			t.Fatal(conversionErr)
		}
		part, marshalErr := json.Marshal(blocks)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		encoded = append(encoded, part...)
	}
	for _, expected := range []string{`"type":"card"`, `"type":"carousel"`, `"type":"data_table"`, `"caption":"Latency"`} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("Slack block conversion dropped %q: %s", expected, encoded)
		}
	}
}

func TestAgentPresentationBlocksDowngradeForOrdinaryMessages(t *testing.T) {
	payloads, err := deliveries.NewRenderer().Render(types.SlackResult{Segments: []types.SlackSegment{
		{Kind: types.SlackSegmentCard, Card: &types.SlackCard{Title: "Deployment", Body: "Healthy"}},
		{Kind: types.SlackSegmentCarousel, Carousel: &types.SlackCarousel{Cards: []types.SlackCard{{Title: "A", Body: "First"}, {Title: "B", Body: "Second"}}}},
		{Kind: types.SlackSegmentTable, Table: &types.SlackTable{Caption: "Latency", PageSize: 5, Columns: []types.SlackTableColumn{{Header: "Service"}, {Header: "P95"}}, Rows: [][]types.SlackTableCell{{{Type: "raw_text", Text: "gateway"}, {Type: "raw_number", Number: 42}}}}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	blocks, err := downgradeAgentUIBlocks(payloads[0].Blocks)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(blocks)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`"type":"card"`, `"type":"carousel"`, `"type":"data_table"`} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("ordinary-message blocks retain unsupported %s: %s", forbidden, encoded)
		}
	}
	for _, expected := range []string{`"type":"section"`, `"type":"table"`, `*Deployment*`, `*A*`} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("ordinary-message downgrade dropped %q: %s", expected, encoded)
		}
	}
}

func TestSlackBlocksFromRenderedPreservesApprovalBlocks(t *testing.T) {
	payloads, err := deliveries.NewRenderer().Render(types.SlackResult{Segments: []types.SlackSegment{{
		Kind: types.SlackSegmentApproval,
		Approval: &types.SlackApproval{
			ID:          "approval-1",
			ActionHash:  "sha256:abcdefghijklmnopqrstuvwxyz",
			ToolID:      "tos_tag_trigger",
			OperationID: "put",
			Risk:        "write",
			Destination: "team/channel",
			Arguments:   map[string]any{"enabled": true, "id": "incident-watch"},
			ExpiresAt:   time.Now().Add(time.Hour),
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
	for _, expected := range []string{`"type":"header"`, `"type":"section"`, `"type":"plain_text"`, `"type":"context"`, `"type":"actions"`, `"action_id":"tos_tag_approval_approve"`} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("Slack block conversion dropped %q: %s", expected, encoded)
		}
	}
}

type fakePostMessage struct {
	channel   string
	calls     int
	updates   int
	statuses  int
	starts    int
	appends   int
	stops     int
	stopErr   error
	statusErr error
	updateTS  string
	messages  []slackapi.Message
	reactions []slackapi.ItemRef
}

type fakeSlackHTTPClient struct {
	do func(*http.Request) (*http.Response, error)
}

func (f fakeSlackHTTPClient) Do(request *http.Request) (*http.Response, error) {
	return f.do(request)
}

func (f *fakePostMessage) AddReactionContext(_ context.Context, _ string, item slackapi.ItemRef) error {
	f.reactions = append(f.reactions, item)
	return nil
}

func (f *fakePostMessage) PostMessageContext(_ context.Context, channel string, _ ...slackapi.MsgOption) (string, string, error) {
	f.channel, f.calls = channel, f.calls+1
	return channel, "200.1", nil
}

func (f *fakePostMessage) UpdateMessageContext(_ context.Context, channel, timestamp string, _ ...slackapi.MsgOption) (string, string, string, error) {
	f.channel, f.updateTS, f.updates = channel, timestamp, f.updates+1
	return channel, timestamp, "", nil
}

func (f *fakePostMessage) SetAssistantThreadsStatusContext(_ context.Context, _ slackapi.AssistantThreadsSetStatusParameters) error {
	f.statuses++
	return f.statusErr
}

func (f *fakePostMessage) StartStreamContext(_ context.Context, channel string, _ ...slackapi.MsgOption) (string, string, error) {
	f.channel, f.starts = channel, f.starts+1
	return channel, "stream.1", nil
}

func (f *fakePostMessage) AppendStreamContext(_ context.Context, channel, timestamp string, _ ...slackapi.MsgOption) (string, string, error) {
	f.channel, f.appends = channel, f.appends+1
	return channel, timestamp, nil
}

func (f *fakePostMessage) StopStreamContext(_ context.Context, channel, timestamp string, _ ...slackapi.MsgOption) (string, string, error) {
	f.channel, f.stops = channel, f.stops+1
	return channel, timestamp, f.stopErr
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

func TestLiveDeliveryUpdatesExistingMessageInsteadOfPosting(t *testing.T) {
	fake := &fakePostMessage{}
	delivery := &LiveDelivery{teamID: "team", api: fake, renderer: deliveries.NewRenderer()}
	request := types.SlackDeliveryRequest{ID: "delivery-update", Destination: types.SlackDestination{TeamID: "team", ChannelID: "channel", ThreadTS: "100.1", UpdateTS: "200.2"}, Result: types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentNotice, Notice: &types.SlackNotice{Tone: "success", Title: "Updated", Message: "The original message was updated."}}}}}
	result, err := delivery.Send(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if fake.calls != 0 || fake.updates != 1 || fake.updateTS != "200.2" || result.MessageTS != "200.2" {
		t.Fatalf("posts=%d updates=%d update_ts=%q result=%#v", fake.calls, fake.updates, fake.updateTS, result)
	}
}

func TestLiveDeliveryDisablesLinkAndMediaUnfurls(t *testing.T) {
	unfurlLinks, unfurlMedia := "", ""
	httpClient := fakeSlackHTTPClient{do: func(request *http.Request) (*http.Response, error) {
		body := `{"ok":false,"error":"unexpected_endpoint"}`
		switch request.URL.Path {
		case "/conversations.history":
			body = `{"ok":true,"messages":[],"has_more":false,"response_metadata":{"next_cursor":""}}`
		case "/chat.postMessage":
			if err := request.ParseForm(); err != nil {
				t.Errorf("parse post-message form: %v", err)
			}
			unfurlLinks, unfurlMedia = request.Form.Get("unfurl_links"), request.Form.Get("unfurl_media")
			body = `{"ok":true,"channel":"channel","ts":"200.1"}`
		default:
			t.Errorf("unexpected Slack endpoint %s", request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}}
	client := slackapi.New("xoxb-test", slackapi.OptionAPIURL("https://slack.test/"), slackapi.OptionHTTPClient(httpClient))
	delivery := &LiveDelivery{teamID: "team", api: client, renderer: deliveries.NewRenderer()}
	_, err := delivery.Send(context.Background(), types.SlackDeliveryRequest{ID: "delivery-links", Destination: types.SlackDestination{TeamID: "team", ChannelID: "channel"}, Result: types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentMRKDWN, Text: "See <https://telemetry.slack.com/archives/C123/p123|the source>."}}}})
	if err != nil {
		t.Fatal(err)
	}
	if unfurlLinks != "false" || unfurlMedia != "false" {
		t.Fatalf("unfurl_links=%q unfurl_media=%q", unfurlLinks, unfurlMedia)
	}
}

func TestLiveDeliveryStartsUpdatesAndFinalizesThinkingSteps(t *testing.T) {
	fake := &fakePostMessage{}
	delivery := &LiveDelivery{teamID: "team", api: fake, renderer: deliveries.NewRenderer()}
	started, err := delivery.StartProgress(context.Background(), types.SlackProgressStartRequest{
		IdempotencyKey: "job-1/progress", TeamID: "team", ChannelID: "channel", ThreadTS: "100.1", JobID: "job-1", RecipientUserID: "user-1", Title: "Tag is working",
		Step: types.SlackProgressStep{ID: "agent-work", Title: "Working on the request", Status: types.SlackProgressInProgress},
	})
	if err != nil || started.MessageTS != "stream.1" || fake.statuses != 1 || fake.starts != 1 {
		t.Fatalf("start=%#v statuses=%d starts=%d err=%v", started, fake.statuses, fake.starts, err)
	}
	updated, err := delivery.UpdateProgress(context.Background(), types.SlackProgressUpdateRequest{TeamID: "team", ChannelID: "channel", MessageTS: started.MessageTS, JobID: "job-1", Step: types.SlackProgressStep{ID: "tool-wiki", Title: "Read Agent Wiki", Status: types.SlackProgressComplete, Sources: []types.SlackProgressSource{{URL: "https://wiki.example/page", Text: "Wiki page"}}}})
	if err != nil || updated.MessageTS != started.MessageTS || fake.appends != 1 {
		t.Fatalf("update=%#v appends=%d err=%v", updated, fake.appends, err)
	}
	result, err := delivery.Send(context.Background(), types.SlackDeliveryRequest{ID: "delivery-1", IdempotencyKey: "job-1/final", Destination: types.SlackDestination{TeamID: "team", ChannelID: "channel", ThreadTS: "100.1", StreamTS: started.MessageTS}, Result: types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentMRKDWN, Text: "Done."}}}})
	if err != nil || result.MessageTS != started.MessageTS || fake.stops != 1 || fake.calls != 0 {
		t.Fatalf("result=%#v stops=%d posts=%d err=%v", result, fake.stops, fake.calls, err)
	}
}

func TestLiveDeliverySendsRequiredRecipientTeamForThinkingSteps(t *testing.T) {
	recipientTeam := ""
	recipientUser := ""
	status := ""
	statusThread := ""
	displayMode := ""
	httpClient := fakeSlackHTTPClient{do: func(request *http.Request) (*http.Response, error) {
		body := `{"ok":false,"error":"unexpected_endpoint"}`
		switch request.URL.Path {
		case "/conversations.replies":
			body = `{"ok":true,"messages":[],"has_more":false,"response_metadata":{"next_cursor":""}}`
		case "/assistant.threads.setStatus":
			if err := request.ParseForm(); err != nil {
				t.Errorf("parse thread-status form: %v", err)
			}
			status, statusThread = request.Form.Get("status"), request.Form.Get("thread_ts")
			body = `{"ok":true}`
		case "/chat.startStream":
			if err := request.ParseForm(); err != nil {
				t.Errorf("parse stream form: %v", err)
			}
			recipientTeam = request.Form.Get("recipient_team_id")
			recipientUser = request.Form.Get("recipient_user_id")
			displayMode = request.Form.Get("task_display_mode")
			body = `{"ok":true,"channel":"channel","ts":"stream.1"}`
		default:
			t.Errorf("unexpected Slack endpoint %s", request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}}
	client := slackapi.New("xoxb-test", slackapi.OptionAPIURL("https://slack.test/"), slackapi.OptionHTTPClient(httpClient))
	delivery := &LiveDelivery{teamID: "team", api: client, renderer: deliveries.NewRenderer()}
	if _, err := delivery.StartProgress(context.Background(), types.SlackProgressStartRequest{IdempotencyKey: "job-1/progress", TeamID: "team", ChannelID: "channel", ThreadTS: "100.1", JobID: "job-1", RecipientUserID: "user-1", Title: "Tag is working", Step: types.SlackProgressStep{ID: "agent-work", Title: "Working", Status: types.SlackProgressInProgress}}); err != nil {
		t.Fatal(err)
	}
	if recipientTeam != "team" {
		t.Fatalf("recipient_team_id=%q", recipientTeam)
	}
	if recipientUser != "user-1" {
		t.Fatalf("recipient_user_id=%q", recipientUser)
	}
	if status != "Organizing…" || statusThread != "100.1" {
		t.Fatalf("status=%q status_thread=%q", status, statusThread)
	}
	if displayMode != "plan" {
		t.Fatalf("task_display_mode=%q", displayMode)
	}
}

func TestLiveDeliveryContinuesWhenTransientAgentStatusFails(t *testing.T) {
	fake := &fakePostMessage{statusErr: errors.New("status unavailable")}
	delivery := &LiveDelivery{teamID: "team", api: fake, renderer: deliveries.NewRenderer()}
	result, err := delivery.StartProgress(context.Background(), types.SlackProgressStartRequest{
		IdempotencyKey: "job-1/progress", TeamID: "team", ChannelID: "channel", ThreadTS: "100.1", JobID: "job-1", RecipientUserID: "user-1", Title: "Tag is working",
		Step: types.SlackProgressStep{ID: "agent-work", Title: "Working", Status: types.SlackProgressInProgress},
	})
	if err != nil || result.MessageTS != "stream.1" || fake.statuses != 1 || fake.starts != 1 {
		t.Fatalf("result=%#v statuses=%d starts=%d err=%v", result, fake.statuses, fake.starts, err)
	}
}

func TestLiveDeliveryStopsChunkStreamWithFinalBlockKitChunk(t *testing.T) {
	markdownText, blocks, chunks, unfurlLinks, unfurlMedia := "", "", "", "", ""
	httpClient := fakeSlackHTTPClient{do: func(request *http.Request) (*http.Response, error) {
		body := `{"ok":false,"error":"unexpected_endpoint"}`
		switch request.URL.Path {
		case "/conversations.history":
			body = `{"ok":true,"messages":[],"has_more":false,"response_metadata":{"next_cursor":""}}`
		case "/chat.stopStream":
			if err := request.ParseForm(); err != nil {
				t.Errorf("parse stop-stream form: %v", err)
			}
			markdownText, blocks, chunks = request.Form.Get("markdown_text"), request.Form.Get("blocks"), request.Form.Get("chunks")
			unfurlLinks, unfurlMedia = request.Form.Get("unfurl_links"), request.Form.Get("unfurl_media")
			body = `{"ok":true,"channel":"channel","ts":"stream.1"}`
		default:
			t.Errorf("unexpected Slack endpoint %s", request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}}
	client := slackapi.New("xoxb-test", slackapi.OptionAPIURL("https://slack.test/"), slackapi.OptionHTTPClient(httpClient))
	delivery := &LiveDelivery{teamID: "team", api: client, renderer: deliveries.NewRenderer()}
	result, err := delivery.Send(context.Background(), types.SlackDeliveryRequest{ID: "delivery-1", Destination: types.SlackDestination{TeamID: "team", ChannelID: "channel", StreamTS: "stream.1"}, Result: types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentMRKDWN, Text: "Done."}}, AgentFooter: &types.SlackAgentFooter{ModelID: "gpt-5.6-luna", ReasoningEffort: "max", TotalTokens: 22_200, DurationMS: 12_400}}})
	if err != nil || result.MessageTS != "stream.1" {
		t.Fatalf("result=%#v err=%v", result, err)
	}
	if markdownText != "" || blocks != "" || !strings.Contains(chunks, `"type":"blocks"`) || !strings.Contains(chunks, `"type":"context"`) || !strings.Contains(chunks, "Done.") || !strings.Contains(chunks, "ChatGPT 5.6 Luna") || !strings.Contains(chunks, "22k tokens") {
		t.Fatalf("markdown_text=%q blocks=%q chunks=%q", markdownText, blocks, chunks)
	}
	if unfurlLinks != "false" || unfurlMedia != "false" {
		t.Fatalf("unfurl_links=%q unfurl_media=%q", unfurlLinks, unfurlMedia)
	}
}

func TestLiveDeliveryFallsBackWhenStreamFinalizationFails(t *testing.T) {
	fake := &fakePostMessage{stopErr: errors.New("stream unsupported")}
	delivery := &LiveDelivery{teamID: "team", api: fake, renderer: deliveries.NewRenderer()}
	result, err := delivery.Send(context.Background(), types.SlackDeliveryRequest{ID: "delivery-fallback", Destination: types.SlackDestination{TeamID: "team", ChannelID: "channel", StreamTS: "stream.1"}, Result: types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentMRKDWN, Text: "Done."}}}})
	if err != nil || result.MessageTS != "200.1" || fake.stops != 1 || fake.calls != 1 || fake.updates != 1 || fake.updateTS != "stream.1" {
		t.Fatalf("result=%#v stops=%d posts=%d updates=%d update_ts=%q err=%v", result, fake.stops, fake.calls, fake.updates, fake.updateTS, err)
	}
}

func TestLiveDeliveryReconcilesExistingThinkingSteps(t *testing.T) {
	fake := &fakePostMessage{messages: []slackapi.Message{{Msg: slackapi.Msg{Timestamp: "stream.existing", Metadata: slackapi.SlackMetadata{EventType: "tos_tag_progress", EventPayload: map[string]any{"idempotency_key": "job-1/progress"}}}}}}
	delivery := &LiveDelivery{teamID: "team", api: fake, renderer: deliveries.NewRenderer()}
	result, err := delivery.StartProgress(context.Background(), types.SlackProgressStartRequest{IdempotencyKey: "job-1/progress", TeamID: "team", ChannelID: "channel", ThreadTS: "100.1", JobID: "job-1", RecipientUserID: "user-1", Title: "Tag is working", Step: types.SlackProgressStep{ID: "agent-work", Title: "Working", Status: types.SlackProgressInProgress}})
	if err != nil || !result.Duplicate || result.MessageTS != "stream.existing" || fake.starts != 0 {
		t.Fatalf("result=%#v starts=%d err=%v", result, fake.starts, err)
	}
}

func TestNormalizeApprovalInteractionBindsAppWorkspaceChannelAndAction(t *testing.T) {
	callback := slackapi.InteractionCallback{
		Type: slackapi.InteractionTypeBlockActions, APIAppID: "A123", Team: slackapi.Team{ID: "T123"},
		Container: slackapi.Container{ChannelID: "C123", MessageTs: "200.2"}, User: slackapi.User{ID: "U123"},
		ActionCallback: slackapi.ActionCallbacks{BlockActions: []*slackapi.BlockAction{{ActionID: "tos_tag_approval_approve", Value: "approval-1"}}},
	}
	interaction, eligible, err := NormalizeApprovalInteraction(LiveOptions{OrganizationID: "org", AppID: "A123", TeamID: "T123"}, callback)
	if err != nil || !eligible || !interaction.Approve || interaction.ApprovalID != "approval-1" || interaction.ChannelID != "C123" || interaction.UserID != "U123" || interaction.MessageTS != "200.2" {
		t.Fatalf("unexpected interaction: %#v eligible=%v err=%v", interaction, eligible, err)
	}
	callback.Team.ID = "T999"
	if _, _, err := NormalizeApprovalInteraction(LiveOptions{OrganizationID: "org", AppID: "A123", TeamID: "T123"}, callback); err == nil {
		t.Fatal("mismatched workspace was accepted")
	}
}

func TestDirectiveCommandAndModalSubmissionRemainChannelBound(t *testing.T) {
	options := LiveOptions{OrganizationID: "org", AppID: "A123", TeamID: "T123"}
	command, eligible, err := NormalizeDirectiveCommand(options, slackapi.SlashCommand{APIAppID: "A123", TeamID: "T123", ChannelID: "C_ALERTS", UserID: "U_ADMIN", Command: directiveSlashCommand, TriggerID: "trigger-1"})
	if err != nil || !eligible || command.Request.ChannelID != "C_ALERTS" || command.Request.UserID != "U_ADMIN" {
		t.Fatalf("command=%#v eligible=%v err=%v", command, eligible, err)
	}
	modal := directiveModal(command.Request, DirectiveConfiguration{Prompt: "Investigate each alert.", Revision: 2})
	if modal.CallbackID != directiveCallbackID || modal.PrivateMetadata == "" || len(modal.Blocks.BlockSet) != 3 {
		t.Fatalf("modal=%#v", modal)
	}
	alert, ok := modal.Blocks.BlockSet[0].(*slackapi.AlertBlock)
	if !ok || alert.Level != slackapi.AlertLevelInfo || !strings.Contains(alert.Text.Text, "only this channel") {
		t.Fatalf("directive scope alert=%#v", modal.Blocks.BlockSet[0])
	}
	callback := slackapi.InteractionCallback{
		Type: slackapi.InteractionTypeViewSubmission, APIAppID: "A123", Team: slackapi.Team{ID: "T123"}, User: slackapi.User{ID: "U_ADMIN"},
		View: slackapi.View{ID: "V123", CallbackID: directiveCallbackID, PrivateMetadata: modal.PrivateMetadata, State: &slackapi.ViewState{Values: map[string]map[string]slackapi.BlockAction{
			directivePromptBlockID: {directivePromptActionID: {Value: "Investigate each alert and determine root cause from OTel."}},
		}}},
	}
	request, eligible, err := NormalizeDirectiveSubmission(options, callback)
	if err != nil || !eligible || request.ChannelID != "C_ALERTS" || request.InteractionID != "V123" || !strings.Contains(request.Prompt, "OTel") {
		t.Fatalf("request=%#v eligible=%v err=%v", request, eligible, err)
	}
	callback.Team.ID = "T999"
	if _, _, err := NormalizeDirectiveSubmission(options, callback); err == nil {
		t.Fatal("cross-workspace directive submission was accepted")
	}
}

func TestModeCommandRemainsWorkspaceBoundAndPassesRequestedMode(t *testing.T) {
	options := LiveOptions{OrganizationID: "org", AppID: "A123", TeamID: "T123"}
	request, eligible, err := NormalizeModeCommand(options, slackapi.SlashCommand{APIAppID: "A123", TeamID: "T123", ChannelID: "C_ALERTS", UserID: "U_ADMIN", Command: modeSlashCommand, Text: " Proactive "})
	if err != nil || !eligible || request.ChannelID != "C_ALERTS" || request.UserID != "U_ADMIN" || request.Mode != "proactive" {
		t.Fatalf("request=%#v eligible=%v err=%v", request, eligible, err)
	}
	if _, eligible, err := NormalizeModeCommand(options, slackapi.SlashCommand{APIAppID: "A123", TeamID: "T123", ChannelID: "C_ALERTS", UserID: "U_ADMIN", Command: directiveSlashCommand}); err != nil || eligible {
		t.Fatalf("directive command leaked into mode normalization: eligible=%v err=%v", eligible, err)
	}
	if _, _, err := NormalizeModeCommand(options, slackapi.SlashCommand{APIAppID: "A123", TeamID: "T999", ChannelID: "C_ALERTS", UserID: "U_ADMIN", Command: modeSlashCommand, Text: "assist"}); err == nil {
		t.Fatal("cross-workspace mode command was accepted")
	}
}
