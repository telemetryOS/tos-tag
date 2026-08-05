package slack

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	slackapi "github.com/slack-go/slack"

	"github.com/telemetryos/tos-tag/core/deliveries"
	"github.com/telemetryos/tos-tag/types"
)

type threadTitleHTTPClient struct {
	t         *testing.T
	calls     int
	channelID string
	threadTS  string
	title     string
}

func (c *threadTitleHTTPClient) Do(request *http.Request) (*http.Response, error) {
	c.t.Helper()
	c.calls++
	if request.URL.Path != "/assistant.threads.setTitle" {
		c.t.Fatalf("unexpected Slack endpoint %s", request.URL.Path)
	}
	if err := request.ParseForm(); err != nil {
		c.t.Fatal(err)
	}
	c.channelID = request.Form.Get("channel_id")
	c.threadTS = request.Form.Get("thread_ts")
	c.title = request.Form.Get("title")
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
	}, nil
}

func TestLiveDeliverySetsAssistantThreadTitleForDirectMessage(t *testing.T) {
	httpClient := &threadTitleHTTPClient{t: t}
	client := slackapi.New("xoxb-test", slackapi.OptionAPIURL("https://slack.test/"), slackapi.OptionHTTPClient(httpClient))
	delivery := &LiveDelivery{teamID: "T123", api: client, renderer: deliveries.NewRenderer()}

	result, err := delivery.SetThreadTitle(context.Background(), types.SlackThreadTitleRequest{
		TeamID: "T123", ChannelID: "D123", ThreadTS: "100.1", SessionID: "session-1", Title: "Investigate ENG-5406",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.UpdatedAt.IsZero() || httpClient.calls != 1 || httpClient.channelID != "D123" || httpClient.threadTS != "100.1" || httpClient.title != "Investigate ENG-5406" {
		t.Fatalf("result=%#v request=%#v", result, httpClient)
	}
}

func TestLiveDeliveryRejectsUnsafeOrNonDirectMessageThreadTitle(t *testing.T) {
	tests := []types.SlackThreadTitleRequest{
		{TeamID: "T999", ChannelID: "D123", ThreadTS: "100.1", SessionID: "session-1", Title: "Valid title"},
		{TeamID: "T123", ChannelID: "C123", ThreadTS: "100.1", SessionID: "session-1", Title: "Valid title"},
		{TeamID: "T123", ChannelID: "D123", ThreadTS: "", SessionID: "session-1", Title: "Valid title"},
		{TeamID: "T123", ChannelID: "D123", ThreadTS: "100.1", SessionID: "", Title: "Valid title"},
		{TeamID: "T123", ChannelID: "D123", ThreadTS: "100.1", SessionID: "session-1", Title: "<@U123> unsafe"},
		{TeamID: "T123", ChannelID: "D123", ThreadTS: "100.1", SessionID: "session-1", Title: "`unsafe"},
		{TeamID: "T123", ChannelID: "D123", ThreadTS: "100.1", SessionID: "session-1", Title: strings.Repeat("x", 81)},
	}
	for index, request := range tests {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			httpClient := &threadTitleHTTPClient{t: t}
			client := slackapi.New("xoxb-test", slackapi.OptionAPIURL("https://slack.test/"), slackapi.OptionHTTPClient(httpClient))
			delivery := &LiveDelivery{teamID: "T123", api: client, renderer: deliveries.NewRenderer()}
			if _, err := delivery.SetThreadTitle(context.Background(), request); err == nil {
				t.Fatal("unsafe thread title request was accepted")
			}
			if httpClient.calls != 0 {
				t.Fatalf("unsafe request made %d Slack calls", httpClient.calls)
			}
		})
	}
}
