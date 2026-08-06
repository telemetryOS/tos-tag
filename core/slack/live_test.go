package slack

import (
	"bytes"
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

	"github.com/telemetryos/tos-tag/core/automations"
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

func TestNormalizeSlackMessageRetainsBoundedImageIdentityWithoutPrivateURL(t *testing.T) {
	callback := slackevents.EventsAPICallbackEvent{TeamID: "team", EventID: "event", EventTime: 100}
	file := slackapi.File{ID: "F123", Name: "screen.png", Mimetype: "image/png", Size: 1024, OriginalW: 640, OriginalH: 480, URLPrivateDownload: "https://files.slack.test/secret"}
	event := slackevents.EventsAPIEvent{Type: slackevents.CallbackEvent, Data: callback, InnerEvent: slackevents.EventsAPIInnerEvent{Data: &slackevents.AppMentionEvent{Channel: "channel", TimeStamp: "100.1", User: "user", Text: "<@bot> what is this?", Files: []slackapi.File{file}}}}
	envelope, eligible, err := NormalizeEventsAPI("org", "bot", "envelope", event)
	if err != nil || !eligible || len(envelope.Images) != 1 {
		t.Fatalf("envelope=%#v eligible=%v err=%v", envelope, eligible, err)
	}
	if envelope.Images[0].FileID != "F123" || envelope.Images[0].MediaType != "image/png" || strings.Contains(fmt.Sprint(envelope.Images[0]), "files.slack") {
		t.Fatalf("image ref = %#v", envelope.Images[0])
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

func TestLiveIngressRecoversOnlyAfterAnActualReconnect(t *testing.T) {
	transport := newFakeSocketModeTransport()
	ingress := &LiveIngress{
		options: LiveOptions{OrganizationID: "org", AppID: "app", TeamID: "team", BotUserID: "bot", Logger: blackbox.New()},
		client:  transport,
	}
	recovered := make(chan struct{}, 2)
	ingress.SetReconnectHandler(func(context.Context) error {
		recovered <- struct{}{}
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := ingress.Start(ctx, func(context.Context, types.SlackEnvelope) (AcceptResult, error) { return AcceptResult{}, nil }); err != nil {
		t.Fatal(err)
	}
	transport.events <- socketmode.Event{Type: socketmode.EventTypeConnected, Data: &socketmode.ConnectedEvent{ConnectionCount: 1}}
	select {
	case <-recovered:
		t.Fatal("initial Socket Mode connection triggered gap recovery")
	case <-time.After(20 * time.Millisecond):
	}
	transport.events <- socketmode.Event{Type: socketmode.EventTypeConnected, Data: &socketmode.ConnectedEvent{ConnectionCount: 2}}
	select {
	case <-recovered:
	case <-time.After(time.Second):
		t.Fatal("Socket Mode reconnect did not trigger gap recovery")
	}
	if err := ingress.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestLiveIngressStopWaitsForReconnectRecovery(t *testing.T) {
	transport := newFakeSocketModeTransport()
	ingress := &LiveIngress{
		options: LiveOptions{OrganizationID: "org", AppID: "app", TeamID: "team", BotUserID: "bot", Logger: blackbox.New()},
		client:  transport,
	}
	recoveryStarted := make(chan struct{})
	releaseRecovery := make(chan struct{})
	ingress.SetReconnectHandler(func(context.Context) error {
		close(recoveryStarted)
		<-releaseRecovery
		return nil
	})
	if err := ingress.Start(context.Background(), func(context.Context, types.SlackEnvelope) (AcceptResult, error) { return AcceptResult{}, nil }); err != nil {
		t.Fatal(err)
	}
	transport.events <- socketmode.Event{Type: socketmode.EventTypeConnected, Data: &socketmode.ConnectedEvent{ConnectionCount: 1}}
	transport.events <- socketmode.Event{Type: socketmode.EventTypeConnected, Data: &socketmode.ConnectedEvent{ConnectionCount: 2}}
	select {
	case <-recoveryStarted:
	case <-time.After(time.Second):
		t.Fatal("reconnect recovery did not start")
	}
	stopped := make(chan error, 1)
	go func() { stopped <- ingress.Stop(context.Background()) }()
	select {
	case err := <-stopped:
		t.Fatalf("Stop returned before recovery completed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(releaseRecovery)
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop did not join reconnect recovery")
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
	payload chan any
	handled chan struct{}
}

type fakeViewAPI struct {
	updated chan slackapi.ModalViewRequest
}

func (f *fakeViewAPI) OpenViewContext(context.Context, string, slackapi.ModalViewRequest) (*slackapi.ViewResponse, error) {
	return &slackapi.ViewResponse{}, nil
}

func (f *fakeViewAPI) UpdateViewContext(_ context.Context, view slackapi.ModalViewRequest, _, _, _ string) (*slackapi.ViewResponse, error) {
	f.updated <- view
	return &slackapi.ViewResponse{}, nil
}

func newFakeSocketModeTransport() *fakeSocketModeTransport {
	return &fakeSocketModeTransport{events: make(chan socketmode.Event, 8), acked: make(chan string, 8), payload: make(chan any, 8), handled: make(chan struct{}, 8)}
}

func (f *fakeSocketModeTransport) RunContext(ctx context.Context) error {
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeSocketModeTransport) AckCtx(_ context.Context, envelopeID string, payload any) error {
	f.acked <- envelopeID
	f.payload <- payload
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
	channel        string
	calls          int
	updates        int
	statuses       int
	stops          int
	stopErr        error
	statusErr      error
	updateTS       string
	messages       []slackapi.Message
	reactions      []slackapi.ItemRef
	statusRequests []slackapi.AssistantThreadsSetStatusParameters
	titleRequests  []slackapi.AssistantThreadsSetTitleParameters
	files          map[string]*slackapi.File
	fileData       map[string][]byte
	uploads        []slackapi.UploadFileParameters
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

func (f *fakePostMessage) SetAssistantThreadsStatusContext(_ context.Context, params slackapi.AssistantThreadsSetStatusParameters) error {
	f.statuses++
	f.statusRequests = append(f.statusRequests, params)
	return f.statusErr
}

func (f *fakePostMessage) SetAssistantThreadsTitleContext(_ context.Context, params slackapi.AssistantThreadsSetTitleParameters) error {
	f.titleRequests = append(f.titleRequests, params)
	return nil
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

func (f *fakePostMessage) GetFileInfoContext(_ context.Context, fileID string, _, _ int) (*slackapi.File, []slackapi.Comment, *slackapi.Paging, error) {
	if file := f.files[fileID]; file != nil {
		return file, nil, nil, nil
	}
	return nil, nil, nil, errors.New("file not found")
}

func (f *fakePostMessage) GetFileContext(_ context.Context, downloadURL string, writer io.Writer) error {
	data, ok := f.fileData[downloadURL]
	if !ok {
		return errors.New("file content not found")
	}
	_, err := writer.Write(data)
	return err
}

func (f *fakePostMessage) UploadFileContext(_ context.Context, params slackapi.UploadFileParameters) (*slackapi.FileSummary, error) {
	f.uploads = append(f.uploads, params)
	return &slackapi.FileSummary{ID: fmt.Sprintf("file-%d", len(f.uploads)), Title: params.Title}, nil
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

func TestLiveDeliveryDownloadsValidatedSlackImageAndUploadsGeneratedFileOnce(t *testing.T) {
	png := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	fake := &fakePostMessage{
		files:    map[string]*slackapi.File{"F1": {ID: "F1", Name: "screen.png", Mimetype: "image/png", Size: len(png), URLPrivateDownload: "https://files.slack.test/F1"}},
		fileData: map[string][]byte{"https://files.slack.test/F1": png},
	}
	delivery := &LiveDelivery{teamID: "team", api: fake, renderer: deliveries.NewRenderer()}
	images, err := delivery.DownloadImages(context.Background(), "team", []types.SlackImageRef{{FileID: "F1", MediaType: "image/png", Size: len(png)}})
	if err != nil || len(images) != 1 || !bytes.Equal(images[0].Data, png) {
		t.Fatalf("images=%#v err=%v", images, err)
	}
	request := types.SlackDeliveryRequest{ID: "delivery-image", Destination: types.SlackDestination{TeamID: "team", ChannelID: "channel"}, Result: types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentMRKDWN, Text: "Generated."}}, Files: []types.SlackFileUpload{{Title: "Generated image", MediaType: "image/png", Data: png}}}}
	if _, err := delivery.Send(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(fake.uploads) != 1 || fake.uploads[0].Filename != "tos-tag-delivery-image-01.png" || fake.uploads[0].Channel != "channel" {
		t.Fatalf("uploads=%#v", fake.uploads)
	}
	fake.messages = []slackapi.Message{{Msg: slackapi.Msg{Files: []slackapi.File{{Name: "tos-tag-delivery-image-01.png"}}, Timestamp: "201.1", Metadata: slackapi.SlackMetadata{EventType: "tos_tag_delivery", EventPayload: map[string]any{"delivery_id": "delivery-image", "part": 1}}}}}
	if _, err := delivery.Send(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(fake.uploads) != 1 {
		t.Fatalf("reconciled upload repeated: %#v", fake.uploads)
	}
}

func TestValidateSlackFileUploadsRejectsAggregateBeyondDurableLimit(t *testing.T) {
	png := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, make([]byte, 7<<20)...)
	err := validateSlackFileUploads([]types.SlackFileUpload{
		{MediaType: "image/png", Data: png},
		{MediaType: "image/png", Data: png},
	})
	if err == nil || err.Error() != "Slack delivery exceeds the generated byte limit" {
		t.Fatalf("err=%v", err)
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

func TestLiveDeliveryUsesNativeAgentStatusWithoutProgressStream(t *testing.T) {
	fake := &fakePostMessage{}
	delivery := &LiveDelivery{teamID: "team", api: fake, renderer: deliveries.NewRenderer()}
	result, err := delivery.SetAgentStatus(context.Background(), types.SlackAgentStatusRequest{
		TeamID: "team", ChannelID: "channel", ThreadTS: "100.1", JobID: "job-1", Status: "Gathering information…",
		LoadingMessages: []string{"Understanding the request…", "Gathering information…", "Planning the next step…"},
	})
	if err != nil || result.UpdatedAt.IsZero() || len(fake.statusRequests) != 1 {
		t.Fatalf("result=%#v status_requests=%#v err=%v", result, fake.statusRequests, err)
	}
	request := fake.statusRequests[0]
	if request.ChannelID != "channel" || request.ThreadTS != "100.1" || request.Status != "Gathering information…" || len(request.LoadingMessages) != 3 {
		t.Fatalf("native agent status = %#v", request)
	}
	delivered, err := delivery.Send(context.Background(), types.SlackDeliveryRequest{ID: "delivery-1", IdempotencyKey: "job-1/final", Destination: types.SlackDestination{TeamID: "team", ChannelID: "channel", ThreadTS: "100.1"}, Result: types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentMRKDWN, Text: "Done."}}}})
	if err != nil || delivered.MessageTS != "200.1" || fake.stops != 0 || fake.calls != 1 {
		t.Fatalf("result=%#v stops=%d posts=%d err=%v", delivered, fake.stops, fake.calls, err)
	}
}

func TestLiveDeliverySendsStatusAndLoadingMessagesToSlack(t *testing.T) {
	status := ""
	statusThread := ""
	loadingMessages := ""
	httpClient := fakeSlackHTTPClient{do: func(request *http.Request) (*http.Response, error) {
		body := `{"ok":false,"error":"unexpected_endpoint"}`
		switch request.URL.Path {
		case "/assistant.threads.setStatus":
			if err := request.ParseForm(); err != nil {
				t.Errorf("parse thread-status form: %v", err)
			}
			status, statusThread, loadingMessages = request.Form.Get("status"), request.Form.Get("thread_ts"), request.Form.Get("loading_messages")
			body = `{"ok":true}`
		default:
			t.Errorf("unexpected Slack endpoint %s", request.URL.Path)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}}
	client := slackapi.New("xoxb-test", slackapi.OptionAPIURL("https://slack.test/"), slackapi.OptionHTTPClient(httpClient))
	delivery := &LiveDelivery{teamID: "team", api: client, renderer: deliveries.NewRenderer()}
	if _, err := delivery.SetAgentStatus(context.Background(), types.SlackAgentStatusRequest{TeamID: "team", ChannelID: "channel", ThreadTS: "100.1", JobID: "job-1", Status: "Gathering information…", LoadingMessages: []string{"Understanding the request…", "Planning the next step…"}}); err != nil {
		t.Fatal(err)
	}
	if status != "Gathering information…" || statusThread != "100.1" {
		t.Fatalf("status=%q status_thread=%q", status, statusThread)
	}
	if loadingMessages != "Understanding the request…,Planning the next step…" {
		t.Fatalf("loading_messages=%q", loadingMessages)
	}
}

func TestLiveDeliveryReportsNativeAgentStatusFailure(t *testing.T) {
	fake := &fakePostMessage{statusErr: errors.New("status unavailable")}
	delivery := &LiveDelivery{teamID: "team", api: fake, renderer: deliveries.NewRenderer()}
	_, err := delivery.SetAgentStatus(context.Background(), types.SlackAgentStatusRequest{TeamID: "team", ChannelID: "channel", ThreadTS: "100.1", JobID: "job-1", Status: "Gathering information…"})
	if err == nil || fake.statuses != 1 {
		t.Fatalf("statuses=%d err=%v", fake.statuses, err)
	}
}

func TestLiveDeliveryRejectsInvalidNativeAgentStatus(t *testing.T) {
	fake := &fakePostMessage{}
	delivery := &LiveDelivery{teamID: "team", api: fake, renderer: deliveries.NewRenderer()}
	_, err := delivery.SetAgentStatus(context.Background(), types.SlackAgentStatusRequest{TeamID: "team", ChannelID: "channel", ThreadTS: "100.1", JobID: "job-1", Status: strings.Repeat("x", 101)})
	if err == nil || fake.statuses != 0 {
		t.Fatalf("statuses=%d err=%v", fake.statuses, err)
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

func TestAutomationCommandPickerAndSubmissionRemainChannelBound(t *testing.T) {
	options := LiveOptions{OrganizationID: "org", AppID: "A123", TeamID: "T123"}
	command, eligible, err := NormalizeAutomationCommand(options, slackapi.SlashCommand{APIAppID: "A123", TeamID: "T123", ChannelID: "C_ALERTS", UserID: "U_ADMIN", Command: automationsSlashCommand, TriggerID: "trigger-command"})
	if err != nil || !eligible || command.ChannelID != "C_ALERTS" || command.ActorID != "U_ADMIN" || command.TriggerID != "trigger-command" {
		t.Fatalf("command=%#v eligible=%v err=%v", command, eligible, err)
	}
	task := automations.Task{Kind: automations.KindHeartbeat, ID: "weekday-check", Instruction: "Check unresolved alerts.", Cron: "0 9 * * 1-5", Timezone: "America/Vancouver", MinConfidence: .8, Enabled: true, Version: 3, Editable: true}
	result := automations.ListResult{Tasks: []automations.Task{task}, Editable: true, DefaultTimezone: "America/Vancouver"}
	picker := automationPickerModal(command.Scope, result)
	if picker.CallbackID != automationPickerCallbackID || picker.PrivateMetadata == "" || len(picker.Blocks.BlockSet) != 2 {
		t.Fatalf("picker=%#v", picker)
	}
	input, ok := picker.Blocks.BlockSet[1].(*slackapi.InputBlock)
	if !ok || input.Element == nil {
		t.Fatalf("automation picker input=%#v", picker.Blocks.BlockSet[1])
	}
	selectElement, ok := input.Element.(*slackapi.SelectBlockElement)
	if !ok || len(selectElement.Options) != 2 || selectElement.Options[0].Text.Text != "Add automation" {
		t.Fatalf("automation picker select=%#v", input.Element)
	}
	pickerSubmit := slackapi.InteractionCallback{
		Type: slackapi.InteractionTypeViewSubmission, APIAppID: "A123", Team: slackapi.Team{ID: "T123"}, User: slackapi.User{ID: "U_ADMIN"},
		View: slackapi.View{ID: "V_PICKER", CallbackID: automationPickerCallbackID, PrivateMetadata: picker.PrivateMetadata, State: &slackapi.ViewState{Values: map[string]map[string]slackapi.BlockAction{
			automationPickerBlockID: {automationPickerActionID: {SelectedOption: *selectElement.Options[1]}},
		}}},
	}
	choice, eligible, err := NormalizeAutomationPickerSubmission(options, pickerSubmit)
	if err != nil || !eligible || choice.ChannelID != "C_ALERTS" || choice.ID != task.ID || choice.Kind != task.Kind || choice.Add || choice.Timezone != "America/Vancouver" {
		t.Fatalf("choice=%#v eligible=%v err=%v", choice, eligible, err)
	}
	modal := automationModal(choice.Scope, task)
	if modal.CallbackID != automationCallbackID || modal.PrivateMetadata == "" || len(modal.Blocks.BlockSet) != 5 {
		t.Fatalf("modal=%#v", modal)
	}
	if first, ok := modal.Blocks.BlockSet[0].(*slackapi.InputBlock); !ok || first.BlockID != automationInstructionID {
		t.Fatalf("automation editor still has an introductory block: %#v", modal.Blocks.BlockSet[0])
	}
	actions, ok := modal.Blocks.BlockSet[4].(*slackapi.ActionBlock)
	if !ok || actions.Elements == nil || len(actions.Elements.ElementSet) != 1 {
		t.Fatalf("automation delete actions=%#v", modal.Blocks.BlockSet[4])
	}
	deleteButton, ok := actions.Elements.ElementSet[0].(*slackapi.ButtonBlockElement)
	if !ok || deleteButton.ActionID != automationDeleteActionID || deleteButton.Style != slackapi.StyleDanger || deleteButton.Confirm == nil || deleteButton.Confirm.Style != slackapi.StyleDanger {
		t.Fatalf("automation delete button=%#v", actions.Elements.ElementSet[0])
	}
	deleteCallback := slackapi.InteractionCallback{
		Type: slackapi.InteractionTypeBlockActions, APIAppID: "A123", Team: slackapi.Team{ID: "T123"}, User: slackapi.User{ID: "U_ADMIN"},
		View:           slackapi.View{ID: "V_DELETE", Hash: "hash-1", CallbackID: automationCallbackID, PrivateMetadata: modal.PrivateMetadata},
		ActionCallback: slackapi.ActionCallbacks{BlockActions: []*slackapi.BlockAction{{ActionID: automationDeleteActionID, Value: deleteButton.Value}}},
	}
	deletion, eligible, err := NormalizeAutomationDeleteInteraction(options, deleteCallback)
	if err != nil || !eligible || deletion.ID != task.ID || deletion.ChannelID != "C_ALERTS" || deletion.Version != task.Version || deletion.ViewID != "V_DELETE" || deletion.ViewHash != "hash-1" {
		t.Fatalf("deletion=%#v eligible=%v err=%v", deletion, eligible, err)
	}
	deleteCallback.Team.ID = "T999"
	if _, _, err := NormalizeAutomationDeleteInteraction(options, deleteCallback); err == nil {
		t.Fatal("cross-workspace automation deletion was accepted")
	}
	submit := slackapi.InteractionCallback{
		Type: slackapi.InteractionTypeViewSubmission, APIAppID: "A123", Team: slackapi.Team{ID: "T123"}, User: slackapi.User{ID: "U_ADMIN"},
		View: slackapi.View{ID: "V123", CallbackID: automationCallbackID, PrivateMetadata: modal.PrivateMetadata, State: &slackapi.ViewState{Values: map[string]map[string]slackapi.BlockAction{
			automationInstructionID: {automationValueActionID: {Value: "Check alerts and page only for unresolved incidents."}},
			automationCronID:        {automationValueActionID: {Value: "30 9 * * 1-5"}},
			automationConfidenceID:  {automationValueActionID: {Value: "0.9"}},
			automationEnabledID:     {automationValueActionID: {SelectedOption: slackapi.OptionBlockObject{Value: "paused"}}},
		}}},
	}
	request, eligible, err := NormalizeAutomationSubmission(options, submit)
	if err != nil || !eligible || request.ChannelID != "C_ALERTS" || request.ID != task.ID || request.Enabled || request.MinConfidence != .9 || request.Version != 3 || request.Timezone != "America/Vancouver" {
		t.Fatalf("request=%#v eligible=%v err=%v", request, eligible, err)
	}
	submit.Team.ID = "T999"
	if _, _, err := NormalizeAutomationSubmission(options, submit); err == nil {
		t.Fatal("cross-workspace automation submission was accepted")
	}
}

func TestAutomationPickerCanOpenANewAutomationWithoutTimezoneInput(t *testing.T) {
	options := LiveOptions{OrganizationID: "org", AppID: "A123", TeamID: "T123"}
	scope := automations.Scope{OrganizationID: "org", WorkspaceID: "T123", ChannelID: "C_MANAGEMENT", ActorID: "U_ADMIN"}
	picker := automationPickerModal(scope, automations.ListResult{Editable: true, DefaultTimezone: "America/Vancouver"})
	input := picker.Blocks.BlockSet[1].(*slackapi.InputBlock)
	selectElement := input.Element.(*slackapi.SelectBlockElement)
	callback := slackapi.InteractionCallback{
		Type: slackapi.InteractionTypeViewSubmission, APIAppID: "A123", Team: slackapi.Team{ID: "T123"}, User: slackapi.User{ID: "U_ADMIN"},
		View: slackapi.View{ID: "V_PICKER", CallbackID: automationPickerCallbackID, PrivateMetadata: picker.PrivateMetadata, State: &slackapi.ViewState{Values: map[string]map[string]slackapi.BlockAction{
			automationPickerBlockID: {automationPickerActionID: {SelectedOption: *selectElement.Options[0]}},
		}}},
	}
	choice, eligible, err := NormalizeAutomationPickerSubmission(options, callback)
	if err != nil || !eligible || !choice.Add || choice.Timezone != "America/Vancouver" {
		t.Fatalf("choice=%#v eligible=%v err=%v", choice, eligible, err)
	}
	task := automations.Task{Kind: automations.KindHeartbeat, Timezone: choice.Timezone, MinConfidence: .8, Enabled: true}
	modal := automationModal(choice.Scope, task)
	if len(modal.Blocks.BlockSet) != 5 {
		t.Fatalf("new automation blocks=%#v", modal.Blocks.BlockSet)
	}
	if first, ok := modal.Blocks.BlockSet[0].(*slackapi.InputBlock); !ok || first.BlockID != automationNameID {
		t.Fatalf("new automation editor still has an introductory block: %#v", modal.Blocks.BlockSet[0])
	}
	for _, block := range modal.Blocks.BlockSet {
		if _, ok := block.(*slackapi.ActionBlock); ok {
			t.Fatal("unsaved automation exposes a delete action")
		}
		if inputBlock, ok := block.(*slackapi.InputBlock); ok && inputBlock.BlockID == "automation_timezone" {
			t.Fatal("timezone input is still visible")
		}
	}
	submit := callback
	submit.View = slackapi.View{ID: "V_NEW", CallbackID: automationCallbackID, PrivateMetadata: modal.PrivateMetadata, State: &slackapi.ViewState{Values: map[string]map[string]slackapi.BlockAction{
		automationNameID:        {automationValueActionID: {Value: "weekday-management-summary"}},
		automationInstructionID: {automationValueActionID: {Value: "Summarize material management updates."}},
		automationCronID:        {automationValueActionID: {Value: "0 17 * * 1-5"}},
		automationConfidenceID:  {automationValueActionID: {Value: "0.8"}},
		automationEnabledID:     {automationValueActionID: {SelectedOption: slackapi.OptionBlockObject{Value: "enabled"}}},
	}}}
	request, eligible, err := NormalizeAutomationSubmission(options, submit)
	if err != nil || !eligible || request.ID != "weekday-management-summary" || request.Version != 0 || request.Timezone != "America/Vancouver" {
		t.Fatalf("request=%#v eligible=%v err=%v", request, eligible, err)
	}
}

func TestLiveIngressDeletesAutomationAndReplacesTheModal(t *testing.T) {
	transport := newFakeSocketModeTransport()
	views := &fakeViewAPI{updated: make(chan slackapi.ModalViewRequest, 1)}
	ingress := &LiveIngress{
		options: LiveOptions{OrganizationID: "org", AppID: "A123", TeamID: "T123", BotUserID: "U_TAG", Logger: blackbox.New()},
		client:  transport,
		views:   views,
	}
	deleted := make(chan automations.DeleteRequest, 1)
	ingress.SetAutomationHandlers(nil, nil, nil, func(_ context.Context, request automations.DeleteRequest) (automations.Task, error) {
		deleted <- request
		return automations.Task{Kind: request.Kind, ID: request.ID, Version: request.Version}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := ingress.Start(ctx, func(context.Context, types.SlackEnvelope) (AcceptResult, error) { return AcceptResult{}, nil }); err != nil {
		t.Fatal(err)
	}
	task := automations.Task{Kind: automations.KindRoutine, ID: "weekday-summary", Instruction: "Summarize.", Cron: "0 17 * * 1-5", Timezone: "America/Vancouver", Enabled: true, Version: 2}
	modal := automationModal(automations.Scope{OrganizationID: "org", WorkspaceID: "T123", ChannelID: "C_MANAGEMENT", ActorID: "U_ADMIN"}, task)
	transport.events <- socketmode.Event{
		Type:    socketmode.EventTypeInteractive,
		Request: &socketmode.Request{EnvelopeID: "delete-envelope"},
		Data: slackapi.InteractionCallback{
			Type: slackapi.InteractionTypeBlockActions, APIAppID: "A123", Team: slackapi.Team{ID: "T123"}, User: slackapi.User{ID: "U_ADMIN"},
			View:           slackapi.View{ID: "V_DELETE", Hash: "hash-1", CallbackID: automationCallbackID, PrivateMetadata: modal.PrivateMetadata},
			ActionCallback: slackapi.ActionCallbacks{BlockActions: []*slackapi.BlockAction{{ActionID: automationDeleteActionID, Value: "delete"}}},
		},
	}
	select {
	case request := <-deleted:
		if request.ChannelID != "C_MANAGEMENT" || request.ID != task.ID || request.Version != task.Version {
			t.Fatalf("delete request=%#v", request)
		}
	case <-time.After(time.Second):
		t.Fatal("automation delete handler was not called")
	}
	select {
	case updated := <-views.updated:
		if updated.Title == nil || updated.Title.Text != "Automation deleted" || updated.Submit != nil || updated.Close == nil || updated.Close.Text != "Close" {
			t.Fatalf("deletion result modal=%#v", updated)
		}
	case <-time.After(time.Second):
		t.Fatal("automation deletion result did not replace the editor")
	}
	if err := ingress.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestAutomationReadOnlyListExplainsMissingEditControls(t *testing.T) {
	task := automations.Task{Kind: automations.KindRoutine, ID: "daily", Instruction: "Review the channel.", Cron: "0 9 * * *", Timezone: "UTC", Enabled: true, Version: 1}
	response := ephemeralAutomationResponse([]automations.Task{task})
	blocks := response["blocks"].([]slackapi.Block)
	header := blocks[0].(*slackapi.SectionBlock)
	section := blocks[1].(*slackapi.SectionBlock)
	if !strings.Contains(header.Text.Text, "read-only access") || section.Accessory != nil {
		t.Fatalf("read-only response=%#v", response)
	}
}

func TestModeCommandRemainsWorkspaceBoundAndPassesRequestedMode(t *testing.T) {
	options := LiveOptions{OrganizationID: "org", AppID: "A123", TeamID: "T123"}
	request, eligible, err := NormalizeModeCommand(options, slackapi.SlashCommand{APIAppID: "A123", TeamID: "T123", ChannelID: "C_ALERTS", UserID: "U_ADMIN", Command: modeSlashCommand, Text: " Proactive "})
	if err != nil || !eligible || request.ChannelID != "C_ALERTS" || request.UserID != "U_ADMIN" || request.Command != modeSlashCommand || request.Mode != "proactive" {
		t.Fatalf("request=%#v eligible=%v err=%v", request, eligible, err)
	}
	if _, eligible, err := NormalizeModeCommand(options, slackapi.SlashCommand{APIAppID: "A123", TeamID: "T123", ChannelID: "C_ALERTS", UserID: "U_ADMIN", Command: directiveSlashCommand}); err != nil || eligible {
		t.Fatalf("directive command leaked into mode normalization: eligible=%v err=%v", eligible, err)
	}
	if _, _, err := NormalizeModeCommand(options, slackapi.SlashCommand{APIAppID: "A123", TeamID: "T999", ChannelID: "C_ALERTS", UserID: "U_ADMIN", Command: modeSlashCommand, Text: "assist"}); err == nil {
		t.Fatal("cross-workspace mode command was accepted")
	}
}

func TestStatusCommandRemainsReadOnlyAndChannelBound(t *testing.T) {
	options := LiveOptions{OrganizationID: "org", AppID: "A123", TeamID: "T123"}
	request, eligible, err := NormalizeStatusCommand(options, slackapi.SlashCommand{APIAppID: "A123", TeamID: "T123", ChannelID: "C_ALERTS", UserID: "U_ADMIN", Command: statusSlashCommand})
	if err != nil || !eligible || request.OrganizationID != "org" || request.WorkspaceID != "T123" || request.ChannelID != "C_ALERTS" || request.UserID != "U_ADMIN" || request.Command != statusSlashCommand || request.Mode != "" {
		t.Fatalf("request=%#v eligible=%v err=%v", request, eligible, err)
	}
	if _, eligible, err := NormalizeStatusCommand(options, slackapi.SlashCommand{APIAppID: "A123", TeamID: "T123", ChannelID: "C_ALERTS", UserID: "U_ADMIN", Command: modeSlashCommand}); err != nil || eligible {
		t.Fatalf("mode command leaked into status normalization: eligible=%v err=%v", eligible, err)
	}
	for name, command := range map[string]slackapi.SlashCommand{
		"cross-workspace": {APIAppID: "A123", TeamID: "T999", ChannelID: "C_ALERTS", UserID: "U_ADMIN", Command: statusSlashCommand},
		"arguments":       {APIAppID: "A123", TeamID: "T123", ChannelID: "C_ALERTS", UserID: "U_ADMIN", Command: statusSlashCommand, Text: "verbose"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := NormalizeStatusCommand(options, command); err == nil {
				t.Fatal("invalid status command was accepted")
			}
		})
	}
}

func TestStatusCommandReturnsEphemeralNativeTableWithoutChangingMode(t *testing.T) {
	transport := newFakeSocketModeTransport()
	ingress := &LiveIngress{
		options: LiveOptions{OrganizationID: "org", TeamID: "team", BotUserID: "U_TAG"},
		client:  transport,
	}
	modeCalls := 0
	ingress.SetModeChangeHandler(func(_ context.Context, request ModeChangeRequest) (ModeChangeResult, error) {
		modeCalls++
		if request.Mode != "" {
			t.Fatalf("status attempted to change mode to %q", request.Mode)
		}
		return ModeChangeResult{Mode: "assist", Previous: "assist", Enrolled: true, Restricted: true, WorkspaceEnabled: true, BotMembershipKnown: true}, nil
	})
	directiveCalls := 0
	ingress.SetDirectiveConfigurationHandlers(func(_ context.Context, request DirectiveConfigurationRequest) (DirectiveConfiguration, error) {
		directiveCalls++
		if request.OrganizationID != "org" || request.WorkspaceID != "team" || request.ChannelID != "C_PRIVATE" || request.UserID != "U_ADMIN" {
			t.Fatalf("directive request=%#v", request)
		}
		return DirectiveConfiguration{Prompt: "Investigate alerts and report evidence.", Revision: 3}, nil
	}, nil)

	ingress.handleStatusCommand(context.Background(), blackbox.New(), "E_STATUS", ModeChangeRequest{OrganizationID: "org", WorkspaceID: "team", ChannelID: "C_PRIVATE", UserID: "U_ADMIN", Command: statusSlashCommand})
	var payload map[string]any
	select {
	case raw := <-transport.payload:
		var ok bool
		payload, ok = raw.(map[string]any)
		if !ok {
			t.Fatalf("ack payload type = %T", raw)
		}
	case <-time.After(time.Second):
		t.Fatal("status command was not acknowledged")
	}
	if modeCalls != 1 || directiveCalls != 1 || payload["response_type"] != "ephemeral" || !strings.Contains(fmt.Sprint(payload["text"]), "assist") {
		t.Fatalf("mode_calls=%d directive_calls=%d payload=%#v", modeCalls, directiveCalls, payload)
	}
	blocks, ok := payload["blocks"].([]slackapi.Block)
	if !ok || len(blocks) != 3 {
		t.Fatalf("status blocks=%#v", payload["blocks"])
	}
	table, ok := blocks[1].(*slackapi.TableBlock)
	if !ok || len(table.Rows) != 5 || len(table.ColumnSettings) != 2 || !table.ColumnSettings[1].IsWrapped {
		t.Fatalf("status table=%#v", blocks[1])
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"type":"table"`, `"Participation"`, `"assist —`, `"Revision 3`, `"Availability"`, `"Enabled"`, `"Tag presence"`, `"Not joined —`, `"Channel scope"`, `"Private or restricted —`, `/tag-directive`, `/tag-automations`} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("status response missing %q: %s", expected, encoded)
		}
	}
}

func TestStatusResponseBoundsDirectiveAndDegradesItsLookupIndependently(t *testing.T) {
	longDirective := strings.Repeat("evidence ", 400)
	response := ephemeralStatusResponse(
		ModeChangeResult{Mode: "proactive", Enrolled: true, WorkspaceEnabled: true, BotMembershipKnown: true, BotIsMember: true},
		DirectiveConfiguration{Prompt: longDirective, Revision: 9},
		true,
	)
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) >= 10000 || !strings.Contains(string(encoded), `Revision 9`) || !strings.Contains(string(encoded), `…`) || !strings.Contains(string(encoded), `proactive`) || !strings.Contains(string(encoded), `Joined`) {
		t.Fatalf("bounded status response=%s", encoded)
	}

	unavailable, err := json.Marshal(ephemeralStatusResponse(
		ModeChangeResult{Mode: "observe", KillSwitched: true, BotMembershipKnown: false},
		DirectiveConfiguration{},
		false,
	))
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`Unavailable`, `workspace disabled`, `channel not enrolled`, `kill switch active`, `membership has not been reconciled`} {
		if !strings.Contains(string(unavailable), expected) {
			t.Fatalf("degraded status response missing %q: %s", expected, unavailable)
		}
	}
}

func TestFixedModeCommandsMapToExistingParticipationModes(t *testing.T) {
	options := LiveOptions{OrganizationID: "org", AppID: "A123", TeamID: "T123"}
	tests := map[string]string{
		proactiveSlashCommand: "proactive",
		assistSlashCommand:    "assist",
		offSlashCommand:       "observe",
	}
	for command, wantMode := range tests {
		t.Run(command, func(t *testing.T) {
			request, eligible, err := NormalizeModeCommand(options, slackapi.SlashCommand{APIAppID: "A123", TeamID: "T123", ChannelID: "C_ALERTS", UserID: "U_ADMIN", Command: command})
			if err != nil || !eligible || request.Command != command || request.Mode != wantMode {
				t.Fatalf("request=%#v eligible=%v err=%v", request, eligible, err)
			}
		})
	}
}

type fakeMembershipAPI struct {
	info       *slackapi.Channel
	infoErr    error
	joinErr    error
	leaveErr   error
	infoCalls  int
	joinCalls  int
	leaveCalls int
	beforeJoin func()
}

func (f *fakeMembershipAPI) GetConversationInfoContext(context.Context, *slackapi.GetConversationInfoInput) (*slackapi.Channel, error) {
	f.infoCalls++
	return f.info, f.infoErr
}

func (f *fakeMembershipAPI) JoinConversationContext(context.Context, string) (*slackapi.Channel, string, []string, error) {
	f.joinCalls++
	if f.beforeJoin != nil {
		f.beforeJoin()
	}
	return &slackapi.Channel{IsMember: f.joinErr == nil}, "", nil, f.joinErr
}

func (f *fakeMembershipAPI) LeaveConversationContext(context.Context, string) (bool, error) {
	f.leaveCalls++
	return f.leaveErr == nil, f.leaveErr
}

func modeAckText(t *testing.T, transport *fakeSocketModeTransport) string {
	t.Helper()
	select {
	case payload := <-transport.payload:
		response, ok := payload.(map[string]any)
		if !ok {
			t.Fatalf("ack payload type = %T", payload)
		}
		return fmt.Sprint(response["text"])
	case <-time.After(time.Second):
		t.Fatal("mode command was not acknowledged")
		return ""
	}
}

func TestFixedProactiveCommandJoinsPublicChannelAndRefreshesMembership(t *testing.T) {
	transport := newFakeSocketModeTransport()
	members := &fakeMembershipAPI{}
	ingress := &LiveIngress{
		options: LiveOptions{OrganizationID: "org", TeamID: "team", BotUserID: "U_TAG"},
		client:  transport,
		members: members,
	}
	var requested []string
	modePersisted := false
	ingress.SetModeChangeHandler(func(_ context.Context, request ModeChangeRequest) (ModeChangeResult, error) {
		requested = append(requested, request.Mode)
		if request.Mode == "" {
			return ModeChangeResult{Mode: "observe", Previous: "observe", Enrolled: true, WorkspaceEnabled: true, BotMembershipKnown: true}, nil
		}
		modePersisted = true
		return ModeChangeResult{Mode: request.Mode, Previous: "observe", Changed: true}, nil
	})
	members.beforeJoin = func() {
		if !modePersisted {
			t.Fatal("channel join happened before participation mode persistence")
		}
	}
	var membership BotMembershipChange
	ingress.SetBotMembershipHandler(func(_ context.Context, change BotMembershipChange) error {
		membership = change
		return nil
	})
	ingress.handleModeCommand(context.Background(), blackbox.New(), "E1", ModeChangeRequest{OrganizationID: "org", WorkspaceID: "team", ChannelID: "C_PUBLIC", UserID: "U_ADMIN", Command: proactiveSlashCommand, Mode: "proactive"})
	text := modeAckText(t, transport)
	if fmt.Sprint(requested) != "[ proactive]" || members.infoCalls != 0 || members.joinCalls != 1 || members.leaveCalls != 0 {
		t.Fatalf("requested=%v info=%d join=%d leave=%d", requested, members.infoCalls, members.joinCalls, members.leaveCalls)
	}
	if !membership.Joined || membership.ChannelID != "C_PUBLIC" || !strings.Contains(text, "*proactive*") || !strings.Contains(text, "also joined") {
		t.Fatalf("membership=%#v response=%q", membership, text)
	}
}

func TestFixedAssistCommandSavesPrivateLevelWithoutPretendingItCanJoin(t *testing.T) {
	transport := newFakeSocketModeTransport()
	members := &fakeMembershipAPI{}
	ingress := &LiveIngress{options: LiveOptions{OrganizationID: "org", TeamID: "team", BotUserID: "U_TAG"}, client: transport, members: members}
	ingress.SetModeChangeHandler(func(_ context.Context, request ModeChangeRequest) (ModeChangeResult, error) {
		if request.Mode == "" {
			return ModeChangeResult{Mode: "observe", Previous: "observe", Enrolled: true, Restricted: true, WorkspaceEnabled: true, BotMembershipKnown: true}, nil
		}
		return ModeChangeResult{Mode: request.Mode, Previous: "observe", Changed: true}, nil
	})
	ingress.handleModeCommand(context.Background(), blackbox.New(), "E2", ModeChangeRequest{OrganizationID: "org", WorkspaceID: "team", ChannelID: "G_PRIVATE", UserID: "U_ADMIN", Command: assistSlashCommand, Mode: "assist"})
	text := modeAckText(t, transport)
	if members.infoCalls != 0 || members.joinCalls != 0 || !strings.Contains(text, "*assist*") || !strings.Contains(text, "private channels") || !strings.Contains(text, "<@U_TAG>") {
		t.Fatalf("info=%d join=%d response=%q", members.infoCalls, members.joinCalls, text)
	}
}

func TestFixedOffCommandPersistsObserveBeforeBestEffortLeave(t *testing.T) {
	transport := newFakeSocketModeTransport()
	members := &fakeMembershipAPI{leaveErr: errors.New("cant_leave_general")}
	ingress := &LiveIngress{options: LiveOptions{OrganizationID: "org", TeamID: "team", BotUserID: "U_TAG"}, client: transport, members: members}
	modeSaved := false
	ingress.SetModeChangeHandler(func(_ context.Context, request ModeChangeRequest) (ModeChangeResult, error) {
		if request.Mode == "" {
			return ModeChangeResult{Mode: "proactive", Previous: "proactive", Enrolled: true, WorkspaceEnabled: true, BotMembershipKnown: true, BotIsMember: true}, nil
		}
		modeSaved = request.Mode == "observe"
		return ModeChangeResult{Mode: request.Mode, Previous: "proactive", Changed: true}, nil
	})
	ingress.handleModeCommand(context.Background(), blackbox.New(), "E3", ModeChangeRequest{OrganizationID: "org", WorkspaceID: "team", ChannelID: "C_GENERAL", UserID: "U_ADMIN", Command: offSlashCommand, Mode: "observe"})
	text := modeAckText(t, transport)
	if !modeSaved || members.leaveCalls != 1 || !strings.Contains(text, "*off* (`observe`)") || !strings.Contains(text, "would not let Tag leave") {
		t.Fatalf("mode_saved=%v leave=%d response=%q", modeSaved, members.leaveCalls, text)
	}
}

func TestFixedActiveCommandDoesNotJoinDisabledChannel(t *testing.T) {
	transport := newFakeSocketModeTransport()
	members := &fakeMembershipAPI{}
	ingress := &LiveIngress{options: LiveOptions{OrganizationID: "org", TeamID: "team", BotUserID: "U_TAG"}, client: transport, members: members}
	calls := 0
	ingress.SetModeChangeHandler(func(_ context.Context, _ ModeChangeRequest) (ModeChangeResult, error) {
		calls++
		return ModeChangeResult{Mode: "observe", Previous: "observe", Enrolled: true, WorkspaceEnabled: false, KillSwitched: true}, nil
	})
	ingress.handleModeCommand(context.Background(), blackbox.New(), "E4", ModeChangeRequest{OrganizationID: "org", WorkspaceID: "team", ChannelID: "C_DISABLED", UserID: "U_ADMIN", Command: proactiveSlashCommand, Mode: "proactive"})
	text := modeAckText(t, transport)
	if calls != 1 || members.joinCalls != 0 || !strings.Contains(text, "cannot be activated") {
		t.Fatalf("calls=%d join=%d response=%q", calls, members.joinCalls, text)
	}
}
