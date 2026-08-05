package pipeline

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/RobertWHurst/blackbox"

	"github.com/telemetryos/tos-tag/core/sessions"
	"github.com/telemetryos/tos-tag/types"
)

type threadTitleDelivery struct {
	requests []types.SlackThreadTitleRequest
	err      error
}

func (d *threadTitleDelivery) Send(context.Context, types.SlackDeliveryRequest) (types.SlackDeliveryResult, error) {
	return types.SlackDeliveryResult{}, nil
}

func (d *threadTitleDelivery) React(context.Context, types.SlackReactionRequest) (types.SlackReactionResult, error) {
	return types.SlackReactionResult{}, nil
}

func (d *threadTitleDelivery) SetAgentStatus(context.Context, types.SlackAgentStatusRequest) (types.SlackAgentStatusResult, error) {
	return types.SlackAgentStatusResult{}, nil
}

func (d *threadTitleDelivery) SetThreadTitle(_ context.Context, request types.SlackThreadTitleRequest) (types.SlackThreadTitleResult, error) {
	d.requests = append(d.requests, request)
	return types.SlackThreadTitleResult{UpdatedAt: time.Now().UTC()}, d.err
}

func TestSafeSlackThreadTitleSanitizesUntrustedRequestText(t *testing.T) {
	tests := map[string]struct {
		input string
		want  string
	}{
		"plain":         {input: "investigate ENG-5406 and propose next steps", want: "Investigate ENG-5406 and propose next steps"},
		"Slack markup":  {input: "<@U123> summarize <https://example.com/private|this link>", want: "Summarize"},
		"code":          {input: "review `Authorization: Bearer abc` behavior", want: "Review behavior"},
		"secret":        {input: "debug token=xoxb-1234567890-secret-value", want: defaultSlackThreadTitle},
		"opaque secret": {input: "check abcdefghijklmnopqrstuvwxyz1234567890", want: defaultSlackThreadTitle},
		"empty":         {input: "<@U123> https://example.com", want: defaultSlackThreadTitle},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			if got := safeSlackThreadTitle(test.input); got != test.want {
				t.Fatalf("title=%q want=%q", got, test.want)
			}
		})
	}
	long := safeSlackThreadTitle(strings.Repeat("word ", 40))
	if len([]rune(long)) > maxSlackThreadTitleRunes || !strings.HasSuffix(long, "…") {
		t.Fatalf("long title=%q runes=%d", long, len([]rune(long)))
	}
}

func TestNewDirectMessageSessionGetsOneBestEffortThreadTitle(t *testing.T) {
	transport := &threadTitleDelivery{}
	p := &Pipeline{deps: Dependencies{Transport: transport, Logger: blackbox.New()}}
	envelope := types.SlackEnvelope{TeamID: "T123", ChannelID: "D123", MessageTS: "100.1", ChannelKind: types.SlackChannelKindDirectMessage, Text: "investigate ENG-5406"}
	session := sessions.Session{ID: "session-1", RootThreadTS: "100.1"}

	p.setNewDirectMessageThreadTitle(context.Background(), envelope, session, true)
	p.setNewDirectMessageThreadTitle(context.Background(), envelope, session, false)
	envelope.ChannelID = "C123"
	envelope.ChannelKind = "channel"
	p.setNewDirectMessageThreadTitle(context.Background(), envelope, session, true)
	if len(transport.requests) != 1 {
		t.Fatalf("title requests=%#v", transport.requests)
	}
	request := transport.requests[0]
	if request.TeamID != "T123" || request.ChannelID != "D123" || request.ThreadTS != "100.1" || request.SessionID != "session-1" || request.Title != "Investigate ENG-5406" {
		t.Fatalf("title request=%#v", request)
	}

	transport.err = errors.New("Slack unavailable")
	p.setNewDirectMessageThreadTitle(context.Background(), types.SlackEnvelope{TeamID: "T123", ChannelID: "D999", MessageTS: "200.1", Text: "plan the task"}, sessions.Session{ID: "session-2"}, true)
	if len(transport.requests) != 2 {
		t.Fatalf("best-effort failure changed request count: %#v", transport.requests)
	}
}

func TestNewDirectMessageJobSetsThreadTitleOnce(t *testing.T) {
	system := newTestSystem(t)
	message := envelope("dm-title", "D123", "300.1", "investigate ENG-5406 in depth and propose next steps")
	message.ChannelKind = types.SlackChannelKindDirectMessage

	if _, err := system.ingress.Inject(context.Background(), message); err != nil {
		t.Fatal(err)
	}
	waitFor(t, func() bool { return len(system.transport.Requests()) == 1 })
	requests := system.transport.ThreadTitleRequests()
	if len(requests) != 1 {
		t.Fatalf("thread title requests=%#v", requests)
	}
	if requests[0].ChannelID != message.ChannelID || requests[0].ThreadTS != message.MessageTS || requests[0].Title != "Investigate ENG-5406 in depth and propose next steps" {
		t.Fatalf("thread title request=%#v", requests[0])
	}

	duplicate, err := system.ingress.Inject(context.Background(), message)
	if err != nil {
		t.Fatal(err)
	}
	if !duplicate.Duplicate {
		t.Fatal("duplicate direct message was not acknowledged as duplicate")
	}
	time.Sleep(10 * time.Millisecond)
	if got := len(system.transport.ThreadTitleRequests()); got != 1 {
		t.Fatalf("duplicate changed thread title request count to %d", got)
	}
}
