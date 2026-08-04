package slack

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	slackapi "github.com/slack-go/slack"

	"github.com/telemetryos/tos-tag/core/sessions"
	"github.com/telemetryos/tos-tag/types"
)

type fakeContextSyncAPI struct {
	channels          []slackapi.Channel
	botChannels       []slackapi.Channel
	botNextCursor     string
	nextCursor        string
	history           map[string][]slackapi.Message
	historyPages      map[string][]*slackapi.GetConversationHistoryResponse
	historyPage       map[string]int
	replies           map[string][]slackapi.Message
	listRateLimits    int
	rateLimitDelay    time.Duration
	historyLimits     map[string]int
	replyLimits       map[string]int
	historyErrors     map[string]error
	replyErrors       map[string]error
	conversationIn    *slackapi.GetConversationsForUserParameters
	botConversationIn *slackapi.GetConversationsForUserParameters
	listCalls         int
	botListCalls      int
	historyCalls      []string
	historyParams     []slackapi.GetConversationHistoryParameters
	historyCallAt     []time.Time
	replyCalls        []string
}

func (f *fakeContextSyncAPI) GetBotConversationsForUserContext(_ context.Context, params *slackapi.GetConversationsForUserParameters) ([]slackapi.Channel, string, error) {
	copy := *params
	f.botConversationIn = &copy
	f.botListCalls++
	if f.botChannels != nil {
		return f.botChannels, f.botNextCursor, nil
	}
	return f.channels, "", nil
}

func (f *fakeContextSyncAPI) GetConversationsForUserContext(_ context.Context, params *slackapi.GetConversationsForUserParameters) ([]slackapi.Channel, string, error) {
	copy := *params
	f.conversationIn = &copy
	f.listCalls++
	if f.listRateLimits > 0 {
		f.listRateLimits--
		return nil, "", &slackapi.RateLimitedError{RetryAfter: f.retryAfter()}
	}
	return f.channels, f.nextCursor, nil
}

func (f *fakeContextSyncAPI) GetConversationHistoryContext(_ context.Context, params *slackapi.GetConversationHistoryParameters) (*slackapi.GetConversationHistoryResponse, error) {
	f.historyCalls = append(f.historyCalls, params.ChannelID)
	f.historyParams = append(f.historyParams, *params)
	f.historyCallAt = append(f.historyCallAt, time.Now())
	if f.historyLimits[params.ChannelID] > 0 {
		f.historyLimits[params.ChannelID]--
		return nil, &slackapi.RateLimitedError{RetryAfter: f.retryAfter()}
	}
	if err := f.historyErrors[params.ChannelID]; err != nil {
		return nil, err
	}
	if pages := f.historyPages[params.ChannelID]; len(pages) > 0 {
		if f.historyPage == nil {
			f.historyPage = make(map[string]int)
		}
		index := f.historyPage[params.ChannelID]
		if index >= len(pages) {
			index = len(pages) - 1
		}
		f.historyPage[params.ChannelID]++
		return pages[index], nil
	}
	return &slackapi.GetConversationHistoryResponse{Messages: f.history[params.ChannelID]}, nil
}

func TestContextSyncCatchUpRepairsOnlyCompletedBotJoinedChannels(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	previous := now.Add(-2 * time.Hour)
	mentionTS := fmt.Sprintf("%d.%06d", now.Add(-time.Hour).Unix(), now.Add(-time.Hour).Nanosecond()/1_000)
	ambientTS := fmt.Sprintf("%d.%06d", now.Add(-30*time.Minute).Unix(), now.Add(-30*time.Minute).Nanosecond()/1_000)
	state := NewMemoryContextSyncStateStore()
	for _, channelID := range []string{"C-joined", "C-observe"} {
		if err := state.CompleteBootstrap(context.Background(), "org", "team", channelID, previous); err != nil {
			t.Fatal(err)
		}
	}
	joined := testSlackChannel("C-joined", "joined", false)
	botJoined := joined
	botJoined.IsMember = true
	api := &fakeContextSyncAPI{
		channels:    []slackapi.Channel{joined, testSlackChannel("C-observe", "observe", false)},
		botChannels: []slackapi.Channel{botJoined},
		history: map[string][]slackapi.Message{
			"C-joined": {
				{Msg: slackapi.Msg{Timestamp: mentionTS, User: "U-human", Text: "status <@U-tag|tag (local)>"}},
				{Msg: slackapi.Msg{Timestamp: ambientTS, User: "U-human", Text: "ambient update"}},
			},
			"C-observe": {{Msg: slackapi.Msg{Timestamp: mentionTS, User: "U-human", Text: "must not be polled"}}},
		},
	}
	syncer, err := newContextSyncerWithAPIs(ContextSyncOptions{
		OrganizationID: "org", TeamID: "team", BotUserID: "U-tag", Lookback: 24 * time.Hour, Timeout: time.Second,
		MaxChannels: 10, MaxMessages: 10, MessagesPerChannel: 10, StateStore: state,
	}, api, api)
	if err != nil {
		t.Fatal(err)
	}
	run, err := syncer.Discover(context.Background(), func(context.Context, types.SlackContextChannel) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	statesBefore, err := state.List(context.Background(), "org", "team")
	if err != nil {
		t.Fatal(err)
	}
	target := statesBefore["C-joined"].CatchUpThrough
	var recovered []types.SlackEnvelope
	stats, err := syncer.CatchUp(context.Background(), run, func(_ context.Context, envelope types.SlackEnvelope) error {
		recovered = append(recovered, envelope)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.ChannelsCaughtUp != 1 || stats.MessagesRecovered != 2 || len(recovered) != 2 {
		t.Fatalf("catch-up stats=%#v recovered=%#v", stats, recovered)
	}
	if len(api.historyCalls) != 1 || api.historyCalls[0] != "C-joined" {
		t.Fatalf("catch-up polled non-member conversations: %v", api.historyCalls)
	}
	if len(api.historyParams) != 1 || api.historyParams[0].Oldest != slackHistoryTimestamp(previous) || api.historyParams[0].Latest != slackHistoryTimestamp(target) {
		t.Fatalf("catch-up history window = %#v", api.historyParams)
	}
	if !recovered[0].IsMention && !recovered[1].IsMention {
		t.Fatalf("direct mention was not recognized: %#v", recovered)
	}
	states, err := state.List(context.Background(), "org", "team")
	if err != nil {
		t.Fatal(err)
	}
	if !states["C-joined"].SyncedThrough.Equal(target) || !states["C-observe"].SyncedThrough.Equal(previous) {
		t.Fatalf("catch-up watermarks = %#v", states)
	}
}

func TestContextSyncCatchUpNeverReplaysInitialHistory(t *testing.T) {
	nowTS := fmt.Sprintf("%d.000001", time.Now().UTC().Unix())
	joined := testSlackChannel("C-new", "new", false)
	botJoined := joined
	botJoined.IsMember = true
	api := &fakeContextSyncAPI{
		channels:    []slackapi.Channel{joined},
		botChannels: []slackapi.Channel{botJoined},
		history:     map[string][]slackapi.Message{"C-new": {{Msg: slackapi.Msg{Timestamp: nowTS, User: "U-human", Text: "old <@U-tag>"}}}},
	}
	syncer, err := newContextSyncerWithAPIs(ContextSyncOptions{
		OrganizationID: "org", TeamID: "team", BotUserID: "U-tag", Lookback: time.Hour, Timeout: time.Second,
		MaxChannels: 10, MaxMessages: 10, MessagesPerChannel: 10, StateStore: NewMemoryContextSyncStateStore(),
	}, api, api)
	if err != nil {
		t.Fatal(err)
	}
	run, err := syncer.Discover(context.Background(), func(context.Context, types.SlackContextChannel) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	stats, err := syncer.CatchUp(context.Background(), run, func(context.Context, types.SlackEnvelope) error {
		t.Fatal("initial history entered catch-up")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.ChannelsCaughtUp != 0 || len(api.historyCalls) != 0 {
		t.Fatalf("initial history was replayed: stats=%#v calls=%v", stats, api.historyCalls)
	}
}

func TestContextSyncCatchUpIncludesAuthorizedDirectMessages(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	previous := now.Add(-time.Hour)
	messageAt := now.Add(-30 * time.Minute)
	messageTS := fmt.Sprintf("%d.%06d", messageAt.Unix(), messageAt.Nanosecond()/1_000)
	state := NewMemoryContextSyncStateStore()
	if err := state.CompleteBootstrap(context.Background(), "org", "team", "D-direct", previous); err != nil {
		t.Fatal(err)
	}
	direct := slackapi.Channel{GroupConversation: slackapi.GroupConversation{Conversation: slackapi.Conversation{ID: "D-direct", IsIM: true, User: "U-human"}}}
	userAPI := &fakeContextSyncAPI{}
	botAPI := &fakeContextSyncAPI{
		botChannels: []slackapi.Channel{direct},
		history:     map[string][]slackapi.Message{"D-direct": {{Msg: slackapi.Msg{Timestamp: messageTS, User: "U-human", Text: "can you catch this up?"}}}},
	}
	syncer, err := newContextSyncerWithAPIs(ContextSyncOptions{
		OrganizationID: "org", TeamID: "team", BotUserID: "U-tag", Lookback: 24 * time.Hour, Timeout: time.Second,
		MaxChannels: 10, MaxMessages: 10, MessagesPerChannel: 10, StateStore: state,
	}, userAPI, botAPI)
	if err != nil {
		t.Fatal(err)
	}
	run, err := syncer.Discover(context.Background(), func(context.Context, types.SlackContextChannel) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	var recovered []types.SlackEnvelope
	if _, err := syncer.CatchUp(context.Background(), run, func(_ context.Context, envelope types.SlackEnvelope) error {
		recovered = append(recovered, envelope)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].ChannelKind != types.SlackChannelKindDirectMessage || recovered[0].IsMention {
		t.Fatalf("recovered direct message = %#v", recovered)
	}
	if len(botAPI.historyCalls) != 1 || botAPI.historyCalls[0] != "D-direct" || len(userAPI.historyCalls) != 0 {
		t.Fatalf("direct-message history calls: bot=%v user=%v", botAPI.historyCalls, userAPI.historyCalls)
	}
}

func TestContextSyncBootstrapUsesBotTokenForBotOnlyDirectMessage(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	messageTS := fmt.Sprintf("%d.%06d", now.Add(-time.Minute).Unix(), now.Add(-time.Minute).Nanosecond()/1_000)
	direct := slackapi.Channel{GroupConversation: slackapi.GroupConversation{Conversation: slackapi.Conversation{ID: "D-bot", IsIM: true, User: "U-human"}}}
	userAPI := &fakeContextSyncAPI{historyErrors: map[string]error{"D-bot": slackapi.SlackErrorResponse{Err: "channel_not_found"}}}
	botAPI := &fakeContextSyncAPI{
		botChannels: []slackapi.Channel{direct},
		history:     map[string][]slackapi.Message{"D-bot": {{Msg: slackapi.Msg{Timestamp: messageTS, User: "U-human", Text: "bootstrap context"}}}},
	}
	syncer, err := newContextSyncerWithAPIs(ContextSyncOptions{
		OrganizationID: "org", TeamID: "team", BotUserID: "U-tag", Lookback: 24 * time.Hour, Timeout: time.Second,
		MaxChannels: 10, MaxMessages: 10, MessagesPerChannel: 10, StateStore: NewMemoryContextSyncStateStore(),
	}, userAPI, botAPI)
	if err != nil {
		t.Fatal(err)
	}
	run, err := syncer.Discover(context.Background(), func(context.Context, types.SlackContextChannel) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	var imported []types.SlackEnvelope
	stats, err := syncer.Backfill(context.Background(), run, func(_ context.Context, envelope types.SlackEnvelope) error {
		imported = append(imported, envelope)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.MessagesImported != 1 || len(imported) != 1 || imported[0].ChannelID != "D-bot" || len(botAPI.historyCalls) != 1 || len(userAPI.historyCalls) != 0 {
		t.Fatalf("bot-DM bootstrap: stats=%#v imported=%#v bot=%v user=%v", stats, imported, botAPI.historyCalls, userAPI.historyCalls)
	}
}

func TestContextSyncCatchUpExcludesUserOnlyDirectMessagesAndClearsStaleCursor(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	previous := now.Add(-time.Hour)
	state := NewMemoryContextSyncStateStore()
	if err := state.CompleteBootstrap(context.Background(), "org", "team", "D-user-only", previous); err != nil {
		t.Fatal(err)
	}
	if err := state.BeginCatchUp(context.Background(), "org", "team", "D-user-only", now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	direct := slackapi.Channel{GroupConversation: slackapi.GroupConversation{Conversation: slackapi.Conversation{ID: "D-user-only", IsIM: true, User: "U-other"}}}
	api := &fakeContextSyncAPI{
		channels:    []slackapi.Channel{direct},
		botChannels: []slackapi.Channel{},
		history:     map[string][]slackapi.Message{"D-user-only": {{Msg: slackapi.Msg{Timestamp: fmt.Sprintf("%d.000001", now.Unix()), User: "U-other", Text: "not a Tag DM"}}}},
	}
	syncer, err := newContextSyncerWithAPIs(ContextSyncOptions{
		OrganizationID: "org", TeamID: "team", BotUserID: "U-tag", Lookback: 24 * time.Hour, Timeout: time.Second,
		MaxChannels: 10, MaxMessages: 10, MessagesPerChannel: 10, StateStore: state,
	}, api, api)
	if err != nil {
		t.Fatal(err)
	}
	run, err := syncer.Discover(context.Background(), func(_ context.Context, channel types.SlackContextChannel) (bool, error) {
		if channel.BotIsMember || !channel.BotMembershipKnown {
			t.Fatalf("user-only DM membership = %#v", channel)
		}
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := syncer.CatchUp(context.Background(), run, func(context.Context, types.SlackEnvelope) error {
		t.Fatal("user-only DM entered actionable catch-up")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	states, err := state.List(context.Background(), "org", "team")
	if err != nil {
		t.Fatal(err)
	}
	if !states["D-user-only"].CatchUpThrough.IsZero() || len(api.historyCalls) != 0 {
		t.Fatalf("user-only DM retained actionable catch-up: state=%#v calls=%v", states["D-user-only"], api.historyCalls)
	}
}

func TestContextSyncExcludesSyntheticSlackbotDMAndClearsStaleCursor(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	state := NewMemoryContextSyncStateStore()
	if err := state.CompleteBootstrap(context.Background(), "org", "team", "D-slackbot", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := state.BeginCatchUp(context.Background(), "org", "team", "D-slackbot", now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	direct := slackapi.Channel{GroupConversation: slackapi.GroupConversation{Conversation: slackapi.Conversation{ID: "D-slackbot", IsIM: true, User: "USLACKBOT"}}}
	api := &fakeContextSyncAPI{channels: []slackapi.Channel{direct}, botChannels: []slackapi.Channel{direct}}
	syncer, err := newContextSyncerWithAPIs(ContextSyncOptions{
		OrganizationID: "org", TeamID: "team", BotUserID: "U-tag", Lookback: 24 * time.Hour, Timeout: time.Second,
		MaxChannels: 10, MaxMessages: 10, MessagesPerChannel: 10, StateStore: state,
	}, api, api)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := syncer.Discover(context.Background(), func(_ context.Context, channel types.SlackContextChannel) (bool, error) {
		if channel.BotIsMember || !channel.BotMembershipKnown {
			t.Fatalf("Slackbot DM membership = %#v", channel)
		}
		return true, nil
	}); err != nil {
		t.Fatal(err)
	}
	states, err := state.List(context.Background(), "org", "team")
	if err != nil {
		t.Fatal(err)
	}
	if !states[direct.ID].CatchUpThrough.IsZero() || len(api.historyCalls) != 0 {
		t.Fatalf("Slackbot DM retained actionable catch-up: state=%#v calls=%v", states[direct.ID], api.historyCalls)
	}
}

func TestContextSyncCatchUpCheckpointsBusyChannelAndContinues(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	previous := now.Add(-2 * time.Hour)
	newest := now.Add(-20 * time.Minute)
	checkpoint := now.Add(-40 * time.Minute)
	oldest := now.Add(-time.Hour)
	timestamp := func(value time.Time) string {
		return fmt.Sprintf("%d.%06d", value.Unix(), value.Nanosecond()/1_000)
	}
	state := NewMemoryContextSyncStateStore()
	for _, channelID := range []string{"C-busy", "C-later"} {
		if err := state.CompleteBootstrap(context.Background(), "org", "team", channelID, previous); err != nil {
			t.Fatal(err)
		}
	}
	busy := testSlackChannel("C-busy", "busy", false)
	later := testSlackChannel("C-later", "later", false)
	busyMember, laterMember := busy, later
	busyMember.IsMember, laterMember.IsMember = true, true
	busyFirst := &slackapi.GetConversationHistoryResponse{
		Messages: []slackapi.Message{
			{Msg: slackapi.Msg{Timestamp: timestamp(newest), User: "U-human", Text: "newest"}},
			{Msg: slackapi.Msg{Timestamp: timestamp(checkpoint), User: "U-human", Text: "checkpoint"}},
		},
		HasMore: true,
	}
	busyFirst.ResponseMetaData.NextCursor = "next"
	api := &fakeContextSyncAPI{
		channels:    []slackapi.Channel{busy, later},
		botChannels: []slackapi.Channel{busyMember, laterMember},
		historyPages: map[string][]*slackapi.GetConversationHistoryResponse{
			"C-busy": {
				busyFirst,
				{Messages: []slackapi.Message{{Msg: slackapi.Msg{Timestamp: timestamp(oldest), User: "U-human", Text: "oldest"}}}},
			},
			"C-later": {{Messages: []slackapi.Message{{Msg: slackapi.Msg{Timestamp: timestamp(newest), User: "U-human", Text: "later"}}}}},
		},
	}
	syncer, err := newContextSyncerWithAPIs(ContextSyncOptions{
		OrganizationID: "org", TeamID: "team", BotUserID: "U-tag", Lookback: 24 * time.Hour, Timeout: time.Second,
		MaxChannels: 10, MaxMessages: 4, MessagesPerChannel: 2, StateStore: state,
	}, api, api)
	if err != nil {
		t.Fatal(err)
	}
	run, err := syncer.Discover(context.Background(), func(context.Context, types.SlackContextChannel) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	target := run.syncThrough
	var recovered []string
	first, err := syncer.CatchUp(context.Background(), run, func(_ context.Context, envelope types.SlackEnvelope) error {
		recovered = append(recovered, envelope.ChannelID+"/"+envelope.MessageTS)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if first.ChannelsDeferred != 1 || first.ChannelsCaughtUp != 1 || len(recovered) != 3 {
		t.Fatalf("first bounded catch-up = %#v recovered=%v", first, recovered)
	}
	states, err := state.List(context.Background(), "org", "team")
	if err != nil {
		t.Fatal(err)
	}
	checkpointDelta := states["C-busy"].CatchUpLatest.Sub(checkpoint)
	if checkpointDelta < 0 {
		checkpointDelta = -checkpointDelta
	}
	if !states["C-busy"].SyncedThrough.Equal(previous) || checkpointDelta > time.Microsecond {
		t.Fatalf("busy checkpoint = %#v", states["C-busy"])
	}
	resumeLatest := slackHistoryTimestamp(states["C-busy"].CatchUpLatest)
	if !states["C-later"].SyncedThrough.Equal(target) {
		t.Fatalf("later channel did not complete = %#v", states["C-later"])
	}

	second, err := syncer.CatchUp(context.Background(), run, func(_ context.Context, envelope types.SlackEnvelope) error {
		recovered = append(recovered, envelope.ChannelID+"/"+envelope.MessageTS)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.ChannelsDeferred != 0 || second.ChannelsCaughtUp != 1 || len(recovered) != 4 {
		t.Fatalf("second bounded catch-up = %#v recovered=%v", second, recovered)
	}
	if len(api.historyParams) < 3 || api.historyParams[2].Latest != resumeLatest {
		t.Fatalf("resumed history params = %#v", api.historyParams)
	}
	states, err = state.List(context.Background(), "org", "team")
	if err != nil {
		t.Fatal(err)
	}
	if !states["C-busy"].SyncedThrough.Equal(target) || !states["C-busy"].CatchUpThrough.IsZero() {
		t.Fatalf("busy catch-up did not complete = %#v", states["C-busy"])
	}
}

func TestContextSyncStateHoldsLiveWatermarkUntilCatchUpCompletes(t *testing.T) {
	state := NewMemoryContextSyncStateStore()
	previous := time.Date(2026, 8, 4, 8, 0, 0, 0, time.UTC)
	target := previous.Add(time.Hour)
	live := target.Add(time.Minute)
	if err := state.CompleteBootstrap(context.Background(), "org", "team", "C-channel", previous); err != nil {
		t.Fatal(err)
	}
	if err := state.BeginCatchUp(context.Background(), "org", "team", "C-channel", target); err != nil {
		t.Fatal(err)
	}
	if err := state.Advance(context.Background(), "org", "team", "C-channel", live); err != nil {
		t.Fatal(err)
	}
	states, _ := state.List(context.Background(), "org", "team")
	if !states["C-channel"].SyncedThrough.Equal(previous) || !states["C-channel"].LiveThrough.Equal(live) {
		t.Fatalf("active catch-up watermark = %#v", states["C-channel"])
	}
	if err := state.CompleteCatchUp(context.Background(), "org", "team", "C-channel", target); err != nil {
		t.Fatal(err)
	}
	states, _ = state.List(context.Background(), "org", "team")
	if !states["C-channel"].SyncedThrough.Equal(live) || !states["C-channel"].LiveThrough.IsZero() {
		t.Fatalf("completed catch-up watermark = %#v", states["C-channel"])
	}
}

func TestContextSyncStateExtendsInterruptedCatchUpForNewStartup(t *testing.T) {
	state := NewMemoryContextSyncStateStore()
	previous := time.Date(2026, 8, 4, 8, 0, 0, 0, time.UTC)
	firstTarget := previous.Add(time.Hour)
	checkpoint := firstTarget.Add(-time.Minute)
	secondTarget := firstTarget.Add(time.Hour)
	threadAt := checkpoint.Add(-time.Minute)
	if err := state.CompleteBootstrap(context.Background(), "org", "team", "C-channel", previous); err != nil {
		t.Fatal(err)
	}
	if err := state.BeginCatchUp(context.Background(), "org", "team", "C-channel", firstTarget); err != nil {
		t.Fatal(err)
	}
	if err := state.CheckpointCatchUp(context.Background(), "org", "team", "C-channel", firstTarget, checkpoint); err != nil {
		t.Fatal(err)
	}
	if err := state.CheckpointThreadCatchUp(context.Background(), "org", "team", "C-channel", firstTarget, "old-thread", threadAt); err != nil {
		t.Fatal(err)
	}

	if err := state.BeginCatchUp(context.Background(), "org", "team", "C-channel", secondTarget); err != nil {
		t.Fatal(err)
	}
	states, err := state.List(context.Background(), "org", "team")
	if err != nil {
		t.Fatal(err)
	}
	got := states["C-channel"]
	if !got.CatchUpThrough.Equal(secondTarget) || !got.CatchUpLatest.Equal(secondTarget) || len(got.CatchUpThreads) != 0 {
		t.Fatalf("extended catch-up state = %#v", got)
	}
	if err := state.CompleteCatchUp(context.Background(), "org", "team", "C-channel", firstTarget); err == nil {
		t.Fatal("stale pre-restart catch-up completed the extended recovery window")
	}
}

func TestContextSyncCatchUpRecoversRepliesInExistingTagThreads(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Microsecond)
	previous := now.Add(-2 * time.Hour)
	rootAt := previous.Add(-time.Hour)
	replyAt := now.Add(-time.Hour)
	timestamp := func(value time.Time) string {
		return fmt.Sprintf("%d.%06d", value.Unix(), value.Nanosecond()/1_000)
	}
	rootTS, replyTS := timestamp(rootAt), timestamp(replyAt)
	state := NewMemoryContextSyncStateStore()
	if err := state.CompleteBootstrap(context.Background(), "org", "team", "C-thread", previous); err != nil {
		t.Fatal(err)
	}
	sessionStore := sessions.NewMemoryStore(func() time.Time { return now })
	if _, _, err := sessionStore.Resolve(context.Background(), "org", "team", "C-thread", rootTS); err != nil {
		t.Fatal(err)
	}
	channel := testSlackChannel("C-thread", "thread", false)
	member := channel
	member.IsMember = true
	api := &fakeContextSyncAPI{
		channels:    []slackapi.Channel{channel},
		botChannels: []slackapi.Channel{member},
		history:     map[string][]slackapi.Message{"C-thread": {}},
		replies: map[string][]slackapi.Message{
			"C-thread/" + rootTS: {
				{Msg: slackapi.Msg{Timestamp: rootTS, User: "U-requester", Text: "old root"}},
				{Msg: slackapi.Msg{Timestamp: replyTS, ThreadTimestamp: rootTS, User: "U-requester", Text: "offline continuation"}},
			},
		},
	}
	syncer, err := newContextSyncerWithAPIs(ContextSyncOptions{
		OrganizationID: "org", TeamID: "team", BotUserID: "U-tag", Lookback: 24 * time.Hour, Timeout: time.Second,
		MaxChannels: 10, MaxMessages: 10, MessagesPerChannel: 10, StateStore: state,
		ActiveThreadRoots: func(ctx context.Context, channelID string, updatedSince time.Time) ([]string, error) {
			return sessionStore.ListRoots(ctx, "org", "team", channelID, updatedSince)
		},
	}, api, api)
	if err != nil {
		t.Fatal(err)
	}
	run, err := syncer.Discover(context.Background(), func(context.Context, types.SlackContextChannel) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	var recovered []types.SlackEnvelope
	stats, err := syncer.CatchUp(context.Background(), run, func(_ context.Context, envelope types.SlackEnvelope) error {
		recovered = append(recovered, envelope)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.ChannelsCaughtUp != 1 || stats.MessagesRecovered != 1 || len(recovered) != 1 {
		t.Fatalf("thread catch-up = %#v recovered=%#v", stats, recovered)
	}
	if recovered[0].MessageTS != replyTS || recovered[0].ThreadTS != rootTS {
		t.Fatalf("recovered active-thread reply = %#v", recovered[0])
	}
	if len(api.replyCalls) != 1 || api.replyCalls[0] != "C-thread/"+rootTS {
		t.Fatalf("active-thread reply calls = %v", api.replyCalls)
	}
}

func TestContextSyncDoesNotReadHistoryForLocallyUnauthorizedChannel(t *testing.T) {
	nowTS := fmt.Sprintf("%d.000001", time.Now().UTC().Unix())
	api := &fakeContextSyncAPI{
		channels: []slackapi.Channel{testSlackChannel("C-allowed", "allowed", false), testSlackChannel("C-denied", "denied", false)},
		history: map[string][]slackapi.Message{
			"C-allowed": {{Msg: slackapi.Msg{Timestamp: nowTS, Text: "allowed"}}},
			"C-denied":  {{Msg: slackapi.Msg{Timestamp: nowTS, Text: "must not be read"}}},
		},
	}
	syncer, err := newContextSyncer(ContextSyncOptions{
		OrganizationID: "org", TeamID: "team", Lookback: time.Hour, Timeout: time.Minute,
		MaxChannels: 10, MaxMessages: 10, MessagesPerChannel: 5,
	}, api)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := syncer.Sync(context.Background(), func(_ context.Context, channel types.SlackContextChannel) (bool, error) {
		return channel.ChannelID == "C-allowed", nil
	}, func(context.Context, types.SlackEnvelope) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if stats.ChannelsRegistered != 1 || len(api.historyCalls) != 1 || api.historyCalls[0] != "C-allowed" {
		t.Fatalf("unauthorized history was requested: stats=%#v calls=%v", stats, api.historyCalls)
	}
}

func TestContextSyncDiscoveryCompletesBeforeHistoryBackfill(t *testing.T) {
	nowTS := fmt.Sprintf("%d.000001", time.Now().UTC().Unix())
	api := &fakeContextSyncAPI{
		channels: []slackapi.Channel{testSlackChannel("C-public", "public", false)},
		history:  map[string][]slackapi.Message{"C-public": {{Msg: slackapi.Msg{Timestamp: nowTS}}}},
	}
	syncer, err := newContextSyncer(ContextSyncOptions{
		OrganizationID: "org", TeamID: "team", Lookback: time.Hour, Timeout: time.Second,
		MaxChannels: 10, MaxMessages: 10, MessagesPerChannel: 5,
	}, api)
	if err != nil {
		t.Fatal(err)
	}
	run, err := syncer.Discover(context.Background(), func(context.Context, types.SlackContextChannel) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	if stats := run.Stats(); stats.ChannelsDiscovered != 1 || stats.ChannelsRegistered != 1 || len(api.historyCalls) != 0 {
		t.Fatalf("discovery performed history work: stats=%#v calls=%v", stats, api.historyCalls)
	}
	stats, err := syncer.Backfill(context.Background(), run, func(context.Context, types.SlackEnvelope) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if stats.MessagesImported != 1 || len(api.historyCalls) != 1 {
		t.Fatalf("backfill did not import discovered history: stats=%#v calls=%v", stats, api.historyCalls)
	}
}

func TestContextSyncBootstrapsEachConversationOnlyOnce(t *testing.T) {
	nowTS := fmt.Sprintf("%d.000001", time.Now().UTC().Unix())
	state := NewMemoryContextSyncStateStore()
	api := &fakeContextSyncAPI{
		channels: []slackapi.Channel{testSlackChannel("C-public", "public", false)},
		history:  map[string][]slackapi.Message{"C-public": {{Msg: slackapi.Msg{Timestamp: nowTS}}}},
	}
	syncer, err := newContextSyncer(ContextSyncOptions{
		OrganizationID: "org", TeamID: "team", Lookback: time.Hour, Timeout: time.Second,
		MaxChannels: 10, MaxMessages: 10, MessagesPerChannel: 5, StateStore: state,
	}, api)
	if err != nil {
		t.Fatal(err)
	}
	register := func(context.Context, types.SlackContextChannel) (bool, error) { return true, nil }
	importMessage := func(context.Context, types.SlackEnvelope) error { return nil }
	first, err := syncer.Sync(context.Background(), register, importMessage)
	if err != nil {
		t.Fatal(err)
	}
	second, err := syncer.Sync(context.Background(), register, importMessage)
	if err != nil {
		t.Fatal(err)
	}
	if first.ChannelsBackfilled != 1 || first.ChannelsCurrent != 0 || second.ChannelsBackfilled != 0 || second.ChannelsCurrent != 1 || len(api.historyCalls) != 1 {
		t.Fatalf("durable bootstrap was repeated: first=%#v second=%#v calls=%v", first, second, api.historyCalls)
	}
}

func TestContextSyncBackfillsOnlyNewConversationAfterInitialBootstrap(t *testing.T) {
	nowTS := fmt.Sprintf("%d.000001", time.Now().UTC().Unix())
	state := NewMemoryContextSyncStateStore()
	if err := state.CompleteBootstrap(context.Background(), "org", "team", "C-current", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	api := &fakeContextSyncAPI{
		channels: []slackapi.Channel{testSlackChannel("C-current", "current", false), testSlackChannel("C-new", "new", false)},
		history:  map[string][]slackapi.Message{"C-new": {{Msg: slackapi.Msg{Timestamp: nowTS}}}},
	}
	syncer, err := newContextSyncer(ContextSyncOptions{
		OrganizationID: "org", TeamID: "team", Lookback: time.Hour, Timeout: time.Second,
		MaxChannels: 10, MaxMessages: 10, MessagesPerChannel: 5, StateStore: state,
	}, api)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := syncer.Sync(context.Background(), func(context.Context, types.SlackContextChannel) (bool, error) { return true, nil }, func(context.Context, types.SlackEnvelope) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if stats.ChannelsCurrent != 1 || stats.ChannelsBackfilled != 1 || len(api.historyCalls) != 1 || api.historyCalls[0] != "C-new" {
		t.Fatalf("existing conversation was fetched again: stats=%#v calls=%v", stats, api.historyCalls)
	}
}

func TestContextSyncProactivelyPacesHistoryRequests(t *testing.T) {
	nowTS := fmt.Sprintf("%d.000001", time.Now().UTC().Unix())
	api := &fakeContextSyncAPI{
		channels: []slackapi.Channel{testSlackChannel("C-one", "one", false), testSlackChannel("C-two", "two", false)},
		history: map[string][]slackapi.Message{
			"C-one": {{Msg: slackapi.Msg{Timestamp: nowTS}}},
			"C-two": {{Msg: slackapi.Msg{Timestamp: nowTS}}},
		},
	}
	interval := 15 * time.Millisecond
	syncer, err := newContextSyncer(ContextSyncOptions{
		OrganizationID: "org", TeamID: "team", Lookback: time.Hour, Timeout: time.Second,
		MaxChannels: 10, MaxMessages: 10, MessagesPerChannel: 5, RequestInterval: interval,
	}, api)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := syncer.Sync(context.Background(), func(context.Context, types.SlackContextChannel) (bool, error) { return true, nil }, func(context.Context, types.SlackEnvelope) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(api.historyCallAt) != 2 || api.historyCallAt[1].Sub(api.historyCallAt[0]) < interval-2*time.Millisecond {
		t.Fatalf("history calls were not proactively paced: %v", api.historyCallAt)
	}
}

func (f *fakeContextSyncAPI) GetConversationRepliesContext(_ context.Context, params *slackapi.GetConversationRepliesParameters) ([]slackapi.Message, bool, string, error) {
	key := params.ChannelID + "/" + params.Timestamp
	f.replyCalls = append(f.replyCalls, key)
	if f.replyLimits[key] > 0 {
		f.replyLimits[key]--
		return nil, false, "", &slackapi.RateLimitedError{RetryAfter: f.retryAfter()}
	}
	if err := f.replyErrors[key]; err != nil {
		return nil, false, "", err
	}
	return f.replies[key], false, "", nil
}

func (f *fakeContextSyncAPI) retryAfter() time.Duration {
	if f.rateLimitDelay > 0 {
		return f.rateLimitDelay
	}
	return time.Millisecond
}

func testSlackChannel(id, name string, restricted bool) slackapi.Channel {
	return slackapi.Channel{GroupConversation: slackapi.GroupConversation{
		Conversation: slackapi.Conversation{ID: id, IsPrivate: restricted},
		Name:         name,
	}, IsChannel: true}
}

func TestContextSyncReconcilesBotMembershipSeparatelyFromUserVisibility(t *testing.T) {
	visibleJoined := testSlackChannel("C-joined", "joined", false)
	visibleNotJoined := testSlackChannel("C-observe", "observe", false)
	botJoined := visibleJoined
	botJoined.IsMember = true
	slackbotDM := testSlackChannel("D-slackbot", "", true)
	slackbotDM.User = "USLACKBOT"
	api := &fakeContextSyncAPI{
		channels:    []slackapi.Channel{visibleJoined, visibleNotJoined},
		botChannels: []slackapi.Channel{botJoined, slackbotDM},
	}
	syncer, err := newContextSyncer(ContextSyncOptions{
		OrganizationID: "org", TeamID: "team", Lookback: time.Hour, Timeout: time.Minute,
		MaxChannels: 10, MaxMessages: 10, MessagesPerChannel: 5,
	}, api)
	if err != nil {
		t.Fatal(err)
	}
	var got []types.SlackContextChannel
	_, err = syncer.Discover(context.Background(), func(_ context.Context, channel types.SlackContextChannel) (bool, error) {
		got = append(got, channel)
		return true, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !got[0].IsChannel || !got[0].BotMembershipKnown || !got[0].BotIsMember || !got[1].BotMembershipKnown || got[1].BotIsMember {
		t.Fatalf("bot membership reconciliation = %#v", got)
	}
	if api.botListCalls != 1 || api.botConversationIn == nil || len(api.botConversationIn.Types) != 3 {
		t.Fatalf("bot membership request = %#v calls=%d", api.botConversationIn, api.botListCalls)
	}
	foundIM := false
	for _, conversationType := range api.botConversationIn.Types {
		if conversationType == "im" {
			foundIM = true
		}
		if conversationType == "mpim" {
			t.Fatalf("bot membership request included group-DM type %q", conversationType)
		}
	}
	if !foundIM {
		t.Fatal("bot membership request omitted direct messages")
	}
	for _, channel := range got {
		if channel.ChannelID == slackbotDM.ID {
			t.Fatalf("synthetic Slackbot DM was registered for history recovery: %#v", channel)
		}
	}
}

func TestContextSyncDiscoversAllConversationTypesAndImportsThreads(t *testing.T) {
	now := time.Now().UTC()
	rootTS := fmt.Sprintf("%d.000001", now.Unix())
	replyTS := fmt.Sprintf("%d.000002", now.Unix())
	privateTS := fmt.Sprintf("%d.000003", now.Unix())
	api := &fakeContextSyncAPI{
		channels: []slackapi.Channel{
			testSlackChannel("C-public", "public", false),
			testSlackChannel("G-private", "private", true),
		},
		history: map[string][]slackapi.Message{
			"C-public":  {{Msg: slackapi.Msg{Timestamp: rootTS, User: "U-human", Text: "root", ReplyCount: 1}}},
			"G-private": {{Msg: slackapi.Msg{Timestamp: privateTS, User: "U-private", Text: "private"}}},
		},
		replies: map[string][]slackapi.Message{
			"C-public/" + rootTS: {
				{Msg: slackapi.Msg{Timestamp: rootTS, User: "U-human", Text: "root"}},
				{Msg: slackapi.Msg{Timestamp: replyTS, ThreadTimestamp: rootTS, BotID: "B-helper", Text: "reply"}},
			},
		},
	}
	syncer, err := newContextSyncer(ContextSyncOptions{
		OrganizationID: "org", TeamID: "team", BotUserID: "U-tag",
		Lookback: 24 * time.Hour, Timeout: time.Minute,
		MaxChannels: 10, MaxMessages: 10, MessagesPerChannel: 5,
	}, api)
	if err != nil {
		t.Fatal(err)
	}
	var channels []types.SlackContextChannel
	var messages []types.SlackEnvelope
	stats, err := syncer.Sync(context.Background(), func(_ context.Context, channel types.SlackContextChannel) (bool, error) {
		channels = append(channels, channel)
		return true, nil
	}, func(_ context.Context, envelope types.SlackEnvelope) error {
		messages = append(messages, envelope)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if stats.ChannelsDiscovered != 2 || stats.ChannelsRegistered != 2 || stats.MessagesImported != 3 {
		t.Fatalf("context sync stats = %#v", stats)
	}
	if api.conversationIn == nil || api.conversationIn.TeamID != "team" || len(api.conversationIn.Types) != 3 {
		t.Fatalf("conversation discovery request = %#v", api.conversationIn)
	}
	for _, conversationType := range api.conversationIn.Types {
		if conversationType == "mpim" {
			t.Fatal("group DMs must be excluded from context discovery")
		}
	}
	if channels[0].Restricted || !channels[1].Restricted {
		t.Fatalf("channel disclosure classes = %#v", channels)
	}
	var foundReply, foundPrivate bool
	for _, message := range messages {
		if message.MessageTS == replyTS {
			foundReply = message.ThreadTS == rootTS && message.BotID == "B-helper" && message.EventID == "message/team/C-public/"+replyTS
		}
		if message.MessageTS == privateTS {
			foundPrivate = message.Restricted
		}
	}
	if !foundReply || !foundPrivate {
		t.Fatalf("thread/private history missing: %#v", messages)
	}
}

func TestContextSyncFailsClosedWhenChannelInventoryExceedsBound(t *testing.T) {
	api := &fakeContextSyncAPI{channels: []slackapi.Channel{testSlackChannel("C1", "one", false)}, nextCursor: "more"}
	syncer, err := newContextSyncer(ContextSyncOptions{
		OrganizationID: "org", TeamID: "team", Lookback: time.Hour, Timeout: time.Minute,
		MaxChannels: 1, MaxMessages: 1, MessagesPerChannel: 1,
	}, api)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := syncer.Sync(context.Background(), func(context.Context, types.SlackContextChannel) (bool, error) { return true, nil }, func(context.Context, types.SlackEnvelope) error { return nil }); err == nil {
		t.Fatal("truncated Slack channel inventory was accepted")
	}
}

func TestContextSyncHonorsSlackRateLimitsWithinBoundedTimeout(t *testing.T) {
	now := time.Now().UTC()
	rootTS := fmt.Sprintf("%d.000001", now.Unix())
	replyTS := fmt.Sprintf("%d.000002", now.Unix())
	replyKey := "C-public/" + rootTS
	api := &fakeContextSyncAPI{
		channels:       []slackapi.Channel{testSlackChannel("C-public", "public", false)},
		history:        map[string][]slackapi.Message{"C-public": {{Msg: slackapi.Msg{Timestamp: rootTS, ReplyCount: 1}}}},
		replies:        map[string][]slackapi.Message{replyKey: {{Msg: slackapi.Msg{Timestamp: rootTS}}, {Msg: slackapi.Msg{Timestamp: replyTS, ThreadTimestamp: rootTS}}}},
		listRateLimits: 1,
		historyLimits:  map[string]int{"C-public": 1},
		replyLimits:    map[string]int{replyKey: 1},
	}
	syncer, err := newContextSyncer(ContextSyncOptions{
		OrganizationID: "org", TeamID: "team", Lookback: time.Hour, Timeout: time.Second,
		MaxChannels: 10, MaxMessages: 10, MessagesPerChannel: 5,
	}, api)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := syncer.Sync(context.Background(), func(context.Context, types.SlackContextChannel) (bool, error) { return true, nil }, func(context.Context, types.SlackEnvelope) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if stats.MessagesImported != 2 || api.listCalls != 2 || len(api.historyCalls) != 2 || len(api.replyCalls) != 2 {
		t.Fatalf("rate-limited sync did not retry exactly once: stats=%#v list=%d history=%v replies=%v", stats, api.listCalls, api.historyCalls, api.replyCalls)
	}
}

func TestContextSyncRateLimitWaitStopsAtSyncDeadline(t *testing.T) {
	api := &fakeContextSyncAPI{listRateLimits: 1, rateLimitDelay: time.Hour}
	syncer, err := newContextSyncer(ContextSyncOptions{
		OrganizationID: "org", TeamID: "team", Lookback: time.Hour, Timeout: 5 * time.Millisecond,
		MaxChannels: 10, MaxMessages: 10, MessagesPerChannel: 5,
	}, api)
	if err != nil {
		t.Fatal(err)
	}
	_, err = syncer.Sync(context.Background(), func(context.Context, types.SlackContextChannel) (bool, error) { return true, nil }, func(context.Context, types.SlackEnvelope) error { return nil })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("rate-limited sync error = %v, want deadline exceeded", err)
	}
}

func TestContextSyncSkipsStaleChannelButContinuesBackfill(t *testing.T) {
	nowTS := fmt.Sprintf("%d.000001", time.Now().UTC().Unix())
	api := &fakeContextSyncAPI{
		channels: []slackapi.Channel{
			testSlackChannel("D-stale", "", true),
			testSlackChannel("C-current", "current", false),
		},
		history:       map[string][]slackapi.Message{"C-current": {{Msg: slackapi.Msg{Timestamp: nowTS}}}},
		historyErrors: map[string]error{"D-stale": slackapi.SlackErrorResponse{Err: "channel_not_found"}},
	}
	syncer, err := newContextSyncer(ContextSyncOptions{
		OrganizationID: "org", TeamID: "team", Lookback: time.Hour, Timeout: time.Second,
		MaxChannels: 10, MaxMessages: 10, MessagesPerChannel: 5,
	}, api)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := syncer.Sync(context.Background(), func(context.Context, types.SlackContextChannel) (bool, error) { return true, nil }, func(context.Context, types.SlackEnvelope) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if stats.ChannelsSkipped != 1 || stats.MessagesImported != 1 || len(api.historyCalls) != 2 {
		t.Fatalf("stale channel did not fail open narrowly: stats=%#v calls=%v", stats, api.historyCalls)
	}
}

func TestContextSyncDoesNotSkipAuthorizationFailures(t *testing.T) {
	api := &fakeContextSyncAPI{
		channels:      []slackapi.Channel{testSlackChannel("C-current", "current", false)},
		historyErrors: map[string]error{"C-current": slackapi.SlackErrorResponse{Err: "missing_scope"}},
	}
	syncer, err := newContextSyncer(ContextSyncOptions{
		OrganizationID: "org", TeamID: "team", Lookback: time.Hour, Timeout: time.Second,
		MaxChannels: 10, MaxMessages: 10, MessagesPerChannel: 5,
	}, api)
	if err != nil {
		t.Fatal(err)
	}
	_, err = syncer.Sync(context.Background(), func(context.Context, types.SlackContextChannel) (bool, error) { return true, nil }, func(context.Context, types.SlackEnvelope) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "missing_scope") {
		t.Fatalf("authorization failure was skipped: %v", err)
	}
}
