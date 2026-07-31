package slack

import (
	"context"
	"testing"

	slackapi "github.com/slack-go/slack"
	"github.com/slack-go/slack/slackevents"

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
	if !envelope.IsMention || envelope.Kind != types.SlackEventMessage || envelope.EventID != "event" {
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

type fakePostMessage struct {
	channel  string
	calls    int
	messages []slackapi.Message
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
