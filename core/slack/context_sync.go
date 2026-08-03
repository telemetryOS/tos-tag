package slack

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RobertWHurst/blackbox"
	slackapi "github.com/slack-go/slack"

	"github.com/telemetryos/tos-tag/types"
)

const contextHistoryPageSize = 100

type ContextSyncOptions struct {
	OrganizationID     string
	TeamID             string
	UserOAuthToken     string
	BotUserOAuthToken  string
	BotUserID          string
	Lookback           time.Duration
	Timeout            time.Duration
	MaxChannels        int
	MaxMessages        int
	MessagesPerChannel int
	RequestInterval    time.Duration
	StateStore         ContextSyncStateStore
	Logger             *blackbox.Logger
}

type ContextSyncStats struct {
	ChannelsDiscovered int
	ChannelsRegistered int
	ChannelsSkipped    int
	ChannelsCurrent    int
	ChannelsBackfilled int
	ChannelsCaughtUp   int
	MessagesImported   int
	MessagesRecovered  int
	StartedAt          time.Time
	CompletedAt        time.Time
}

// ContextSyncRun is the immutable result of channel discovery and policy
// registration. Its channel inventory stays private to the Slack adapter so
// callers cannot use the User OAuth client for unrelated operations.
type ContextSyncRun struct {
	stats           ContextSyncStats
	channels        []slackapi.Channel
	catchUpChannels []slackapi.Channel
	cutoff          time.Time
	syncThrough     time.Time
}

type ContextChannelHandler func(context.Context, types.SlackContextChannel) (bool, error)
type ContextMessageHandler func(context.Context, types.SlackEnvelope) error

type contextSyncAPI interface {
	GetConversationsForUserContext(context.Context, *slackapi.GetConversationsForUserParameters) ([]slackapi.Channel, string, error)
	GetConversationHistoryContext(context.Context, *slackapi.GetConversationHistoryParameters) (*slackapi.GetConversationHistoryResponse, error)
	GetConversationRepliesContext(context.Context, *slackapi.GetConversationRepliesParameters) ([]slackapi.Message, bool, string, error)
}

type botContextAPI interface {
	GetConversationsContext(context.Context, *slackapi.GetConversationsParameters) ([]slackapi.Channel, string, error)
}

type contextChannelPage struct {
	channels   []slackapi.Channel
	nextCursor string
}

type contextReplyPage struct {
	messages   []slackapi.Message
	hasMore    bool
	nextCursor string
}

// ContextSyncer uses the separately consented User OAuth Token to discover the
// user's conversations and import a bounded, fair recent-history snapshot.
// It never posts, reacts, joins a channel, or changes Slack state.
type ContextSyncer struct {
	options     ContextSyncOptions
	api         contextSyncAPI
	botAPI      botContextAPI
	backfillMu  sync.Mutex
	paceMu      sync.Mutex
	nextRequest map[string]time.Time
}

func NewContextSyncer(options ContextSyncOptions) (*ContextSyncer, error) {
	if !strings.HasPrefix(options.UserOAuthToken, "xoxp-") {
		return nil, errors.New("Slack context sync requires a User OAuth xoxp token")
	}
	if !strings.HasPrefix(options.BotUserOAuthToken, "xoxb-") {
		return nil, errors.New("Slack context sync requires a Bot User OAuth xoxb token for membership reconciliation")
	}
	return newContextSyncerWithAPIs(options, slackapi.New(options.UserOAuthToken), slackapi.New(options.BotUserOAuthToken))
}

func newContextSyncer(options ContextSyncOptions, api contextSyncAPI) (*ContextSyncer, error) {
	botAPI, ok := api.(botContextAPI)
	if !ok {
		return nil, errors.New("Slack context sync bot membership API is required")
	}
	return newContextSyncerWithAPIs(options, api, botAPI)
}

func newContextSyncerWithAPIs(options ContextSyncOptions, api contextSyncAPI, botAPI botContextAPI) (*ContextSyncer, error) {
	if options.OrganizationID == "" || options.TeamID == "" || options.Lookback <= 0 || options.Timeout <= 0 || options.RequestInterval < 0 || options.MaxChannels <= 0 || options.MaxMessages <= 0 || options.MessagesPerChannel <= 0 || api == nil {
		return nil, errors.New("invalid Slack context sync options")
	}
	if botAPI == nil {
		return nil, errors.New("Slack context sync bot membership API is required")
	}
	if options.Logger == nil {
		options.Logger = blackbox.New()
	}
	if options.StateStore == nil {
		options.StateStore = NewMemoryContextSyncStateStore()
	}
	return &ContextSyncer{options: options, api: api, botAPI: botAPI, nextRequest: make(map[string]time.Time)}, nil
}

func (s *ContextSyncer) Sync(parent context.Context, register ContextChannelHandler, importMessage ContextMessageHandler) (ContextSyncStats, error) {
	if register == nil || importMessage == nil {
		return ContextSyncStats{}, errors.New("Slack context sync handlers are required")
	}
	run, err := s.Discover(parent, register)
	if err != nil {
		return run.Stats(), err
	}
	return s.Backfill(parent, run, importMessage)
}

// Discover authorizes and registers the complete bounded conversation
// inventory. Callers can safely start live ingress after this succeeds; slow
// history import is a separate phase so Slack rate limits cannot create an
// event-capture gap during startup.
func (s *ContextSyncer) Discover(parent context.Context, register ContextChannelHandler) (*ContextSyncRun, error) {
	stats := ContextSyncStats{StartedAt: time.Now().UTC()}
	run := &ContextSyncRun{stats: stats, cutoff: stats.StartedAt.Add(-s.options.Lookback), syncThrough: stats.StartedAt}
	if register == nil {
		return run, errors.New("Slack context channel handler is required")
	}
	ctx, cancel := context.WithTimeout(parent, s.options.Timeout)
	defer cancel()
	s.options.Logger.WithCtx(blackbox.Ctx{
		"organization_id": s.options.OrganizationID,
		"max_channels":    s.options.MaxChannels,
		"max_messages":    s.options.MaxMessages,
		"lookback_hours":  int(s.options.Lookback.Hours()),
	}).Info("Slack user context sync started")

	channels, err := s.listChannels(ctx)
	if err != nil {
		return run, err
	}
	botMembership, err := s.listBotMembership(ctx)
	if err != nil {
		return run, err
	}
	run.stats.ChannelsDiscovered = len(channels)
	registeredChannels := make([]slackapi.Channel, 0, len(channels))
	catchUpChannels := make([]slackapi.Channel, 0)
	for _, channel := range channels {
		authorized, err := register(ctx, contextChannel(s.options.OrganizationID, s.options.TeamID, channel, botMembership))
		if err != nil {
			return run, fmt.Errorf("register Slack context channel %s: %w", channel.ID, err)
		}
		if authorized {
			registeredChannels = append(registeredChannels, channel)
			// Only bot-joined channels can produce Slack output. Restrict the
			// frequent missed-event repair pass to those channels so hundreds of
			// observe-only conversations do not create a polling workload.
			if (channel.IsChannel || channel.IsGroup) && botMembership[channel.ID] {
				catchUpChannels = append(catchUpChannels, channel)
			}
			run.stats.ChannelsRegistered++
		}
	}
	run.channels = registeredChannels
	run.catchUpChannels = catchUpChannels
	s.options.Logger.WithCtx(blackbox.Ctx{
		"organization_id":     s.options.OrganizationID,
		"channels_discovered": run.stats.ChannelsDiscovered,
		"channels_registered": run.stats.ChannelsRegistered,
		"duration_ms":         time.Since(run.stats.StartedAt).Milliseconds(),
	}).Info("Slack user context discovery completed")
	return run, nil
}

// CatchUp repairs Socket Mode delivery gaps for conversations where Tag is a
// member. It only scans conversations with a completed bootstrap and starts
// strictly after their durable live watermark, so first-time history remains
// context-only and old mentions are never replayed as work. The caller decides
// which recovered messages are direct enough to re-enter the decision queue.
func (s *ContextSyncer) CatchUp(parent context.Context, run *ContextSyncRun, recoverMessage ContextMessageHandler) (ContextSyncStats, error) {
	if run == nil || recoverMessage == nil {
		return ContextSyncStats{}, errors.New("Slack context catch-up run and message handler are required")
	}
	s.backfillMu.Lock()
	defer s.backfillMu.Unlock()
	ctx, cancel := context.WithTimeout(parent, s.options.Timeout)
	defer cancel()
	stats := run.stats
	states, err := s.options.StateStore.List(ctx, s.options.OrganizationID, s.options.TeamID)
	if err != nil {
		return stats, err
	}

	remaining := s.options.MaxMessages
	for index, channel := range run.catchUpChannels {
		state, ok := states[channel.ID]
		if !ok || !state.BootstrapCompleted || !state.SyncedThrough.Before(run.syncThrough) {
			continue
		}
		if remaining <= 0 {
			break
		}
		channelsLeft := len(run.catchUpChannels) - index
		fairShare := (remaining + channelsLeft - 1) / channelsLeft
		budget := min(s.options.MessagesPerChannel, fairShare)
		after := state.SyncedThrough
		if after.Before(run.cutoff) {
			after = run.cutoff
		}
		recovered, complete, err := s.backfillChannel(ctx, channel, after, run.syncThrough, budget, recoverMessage)
		if err != nil {
			if code, recoverable := recoverableContextChannelError(err); recoverable {
				stats.ChannelsSkipped++
				s.options.Logger.WithCtx(blackbox.Ctx{
					"organization_id": s.options.OrganizationID,
					"channel_id":      channel.ID,
					"error_code":      code,
				}).Warn("Slack catch-up channel became inaccessible; retaining prior watermark")
				continue
			}
			return stats, fmt.Errorf("catch up Slack context channel %s: %w", channel.ID, err)
		}
		if !complete {
			return stats, fmt.Errorf("catch up Slack context channel %s exceeded its safe message bound; prior watermark retained", channel.ID)
		}
		if err := s.options.StateStore.Advance(ctx, s.options.OrganizationID, s.options.TeamID, channel.ID, run.syncThrough); err != nil {
			return stats, err
		}
		stats.ChannelsCaughtUp++
		stats.MessagesRecovered += recovered
		remaining -= recovered
	}
	stats.CompletedAt = time.Now().UTC()
	s.options.Logger.WithCtx(blackbox.Ctx{
		"organization_id":    s.options.OrganizationID,
		"channels_caught_up": stats.ChannelsCaughtUp,
		"messages_recovered": stats.MessagesRecovered,
		"duration_ms":        stats.CompletedAt.Sub(stats.StartedAt).Milliseconds(),
	}).Info("Slack direct-message catch-up completed")
	return stats, nil
}

func (s *ContextSyncer) listBotMembership(ctx context.Context) (map[string]bool, error) {
	membership := make(map[string]bool)
	cursor := ""
	count := 0
	for {
		limit := min(200, s.options.MaxChannels-count)
		if limit <= 0 {
			return nil, fmt.Errorf("Slack bot channel count exceeds configured maximum %d", s.options.MaxChannels)
		}
		result, err := withSlackRateLimitRetry(ctx, s, "conversations.list", func() (contextChannelPage, error) {
			page, next, callErr := s.botAPI.GetConversationsContext(ctx, &slackapi.GetConversationsParameters{
				Cursor: cursor, Types: []string{"public_channel", "private_channel"}, Limit: limit, ExcludeArchived: true, TeamID: s.options.TeamID,
			})
			return contextChannelPage{channels: page, nextCursor: next}, callErr
		})
		if err != nil {
			return nil, fmt.Errorf("list bot conversations: %w", err)
		}
		count += len(result.channels)
		for _, channel := range result.channels {
			membership[channel.ID] = channel.IsMember
		}
		if result.nextCursor == "" {
			return membership, nil
		}
		if count >= s.options.MaxChannels {
			return nil, fmt.Errorf("Slack bot channel count exceeds configured maximum %d", s.options.MaxChannels)
		}
		cursor = result.nextCursor
	}
}

// Backfill imports a bounded, idempotent history snapshot for a previously
// authorized discovery run. It may run concurrently with live Socket Mode
// ingestion because both paths share canonical event IDs.
func (s *ContextSyncer) Backfill(parent context.Context, run *ContextSyncRun, importMessage ContextMessageHandler) (ContextSyncStats, error) {
	if run == nil || importMessage == nil {
		return ContextSyncStats{}, errors.New("Slack context backfill run and message handler are required")
	}
	s.backfillMu.Lock()
	defer s.backfillMu.Unlock()
	ctx, cancel := context.WithTimeout(parent, s.options.Timeout)
	defer cancel()
	stats := run.stats
	states, err := s.options.StateStore.List(ctx, s.options.OrganizationID, s.options.TeamID)
	if err != nil {
		return stats, err
	}

	remaining := s.options.MaxMessages
	for index, channel := range run.channels {
		if states[channel.ID].BootstrapCompleted {
			stats.ChannelsCurrent++
			continue
		}
		if remaining <= 0 {
			break
		}
		channelsLeft := len(run.channels) - index
		fairShare := (remaining + channelsLeft - 1) / channelsLeft
		budget := min(s.options.MessagesPerChannel, fairShare)
		imported, _, err := s.backfillChannel(ctx, channel, run.cutoff, run.syncThrough, budget, importMessage)
		if err != nil {
			if code, recoverable := recoverableContextChannelError(err); recoverable {
				stats.ChannelsSkipped++
				if stateErr := s.options.StateStore.CompleteBootstrap(ctx, s.options.OrganizationID, s.options.TeamID, channel.ID, run.syncThrough); stateErr != nil {
					return stats, stateErr
				}
				s.options.Logger.WithCtx(blackbox.Ctx{
					"organization_id": s.options.OrganizationID,
					"channel_id":      channel.ID,
					"error_code":      code,
				}).Warn("Slack context channel became inaccessible during backfill; skipping")
				continue
			}
			return stats, fmt.Errorf("backfill Slack context channel %s: %w", channel.ID, err)
		}
		if err := s.options.StateStore.CompleteBootstrap(ctx, s.options.OrganizationID, s.options.TeamID, channel.ID, run.syncThrough); err != nil {
			return stats, err
		}
		stats.ChannelsBackfilled++
		stats.MessagesImported += imported
		remaining -= imported
	}
	stats.CompletedAt = time.Now().UTC()
	s.options.Logger.WithCtx(blackbox.Ctx{
		"organization_id":     s.options.OrganizationID,
		"channels_discovered": stats.ChannelsDiscovered,
		"channels_registered": stats.ChannelsRegistered,
		"channels_skipped":    stats.ChannelsSkipped,
		"channels_current":    stats.ChannelsCurrent,
		"channels_backfilled": stats.ChannelsBackfilled,
		"messages_imported":   stats.MessagesImported,
		"duration_ms":         stats.CompletedAt.Sub(stats.StartedAt).Milliseconds(),
	}).Info("Slack user context sync completed")
	return stats, nil
}

func (r *ContextSyncRun) Stats() ContextSyncStats {
	if r == nil {
		return ContextSyncStats{}
	}
	return r.stats
}

func (s *ContextSyncer) listChannels(ctx context.Context) ([]slackapi.Channel, error) {
	var channels []slackapi.Channel
	cursor := ""
	for {
		limit := min(200, s.options.MaxChannels-len(channels))
		if limit <= 0 {
			return nil, fmt.Errorf("Slack context channel count exceeds configured maximum %d", s.options.MaxChannels)
		}
		result, err := withSlackRateLimitRetry(ctx, s, "users.conversations", func() (contextChannelPage, error) {
			// Group DMs (mpim) are deliberately excluded: tos-tag ignores them
			// across discovery, ingress, and coverage.
			page, next, callErr := s.api.GetConversationsForUserContext(ctx, &slackapi.GetConversationsForUserParameters{
				Cursor: cursor, Types: []string{"public_channel", "private_channel", "im"},
				Limit: limit, ExcludeArchived: true, TeamID: s.options.TeamID,
			})
			return contextChannelPage{channels: page, nextCursor: next}, callErr
		})
		if err != nil {
			return nil, fmt.Errorf("list user conversations: %w", err)
		}
		channels = append(channels, result.channels...)
		if result.nextCursor == "" {
			return channels, nil
		}
		if len(channels) >= s.options.MaxChannels {
			return nil, fmt.Errorf("Slack context channel count exceeds configured maximum %d", s.options.MaxChannels)
		}
		cursor = result.nextCursor
	}
}

func (s *ContextSyncer) backfillChannel(ctx context.Context, channel slackapi.Channel, after, through time.Time, budget int, importMessage ContextMessageHandler) (int, bool, error) {
	if budget <= 0 {
		return 0, false, nil
	}
	restricted := channelRestricted(channel)
	oldest := slackHistoryTimestamp(after)
	latest := slackHistoryTimestamp(through)
	rootBudget := budget
	if budget >= 4 {
		rootBudget = budget * 3 / 4
	}
	var roots []slackapi.Message
	imported := 0
	cursor := ""
	rootsComplete := false
	for imported < rootBudget {
		pageLimit := min(contextHistoryPageSize, rootBudget-imported)
		page, err := withSlackRateLimitRetry(ctx, s, "conversations.history", func() (*slackapi.GetConversationHistoryResponse, error) {
			return s.api.GetConversationHistoryContext(ctx, &slackapi.GetConversationHistoryParameters{
				ChannelID: channel.ID, Cursor: cursor, Limit: pageLimit, Oldest: oldest, Latest: latest,
			})
		})
		if err != nil {
			return imported, false, err
		}
		for _, message := range page.Messages {
			if imported >= rootBudget {
				break
			}
			if !historyMessageEligible(message, after, through) {
				continue
			}
			if err := importMessage(ctx, s.historyEnvelope(channel.ID, restricted, message)); err != nil {
				return imported, false, err
			}
			roots = append(roots, message)
			imported++
		}
		cursor = page.ResponseMetaData.NextCursor
		if !page.HasMore || cursor == "" || len(page.Messages) == 0 {
			rootsComplete = true
			break
		}
	}

	repliesComplete := true
	for _, root := range roots {
		if imported >= budget {
			repliesComplete = false
			break
		}
		if root.ReplyCount <= 0 || root.Timestamp == "" {
			continue
		}
		replyCursor := ""
		for imported < budget {
			pageLimit := min(contextHistoryPageSize, budget-imported+1)
			page, err := withSlackRateLimitRetry(ctx, s, "conversations.replies", func() (contextReplyPage, error) {
				replies, hasMore, next, callErr := s.api.GetConversationRepliesContext(ctx, &slackapi.GetConversationRepliesParameters{
					ChannelID: channel.ID, Timestamp: root.Timestamp, Cursor: replyCursor,
					Limit: pageLimit, Oldest: oldest, Latest: latest,
				})
				return contextReplyPage{messages: replies, hasMore: hasMore, nextCursor: next}, callErr
			})
			if err != nil {
				if code, recoverable := recoverableContextThreadError(err); recoverable {
					s.options.Logger.WithCtx(blackbox.Ctx{
						"organization_id": s.options.OrganizationID,
						"channel_id":      channel.ID,
						"thread_ts":       root.Timestamp,
						"error_code":      code,
					}).Warn("Slack context thread became inaccessible during backfill; skipping")
					break
				}
				return imported, false, err
			}
			for _, reply := range page.messages {
				if imported >= budget {
					break
				}
				if reply.Timestamp == root.Timestamp || !historyMessageEligible(reply, after, through) {
					continue
				}
				if reply.ThreadTimestamp == "" {
					reply.ThreadTimestamp = root.Timestamp
				}
				if err := importMessage(ctx, s.historyEnvelope(channel.ID, restricted, reply)); err != nil {
					return imported, false, err
				}
				imported++
			}
			if !page.hasMore || page.nextCursor == "" || len(page.messages) == 0 {
				break
			}
			if imported >= budget {
				repliesComplete = false
				break
			}
			replyCursor = page.nextCursor
		}
	}
	return imported, rootsComplete && repliesComplete, nil
}

func recoverableContextChannelError(err error) (string, bool) {
	code := slackAPIErrorCode(err)
	switch code {
	case "channel_not_found", "not_in_channel", "is_archived":
		return code, true
	default:
		return code, false
	}
}

func recoverableContextThreadError(err error) (string, bool) {
	code := slackAPIErrorCode(err)
	switch code {
	case "thread_not_found", "message_not_found":
		return code, true
	default:
		return code, false
	}
}

func slackAPIErrorCode(err error) string {
	var value slackapi.SlackErrorResponse
	if errors.As(err, &value) {
		return value.Err
	}
	var pointer *slackapi.SlackErrorResponse
	if errors.As(err, &pointer) && pointer != nil {
		return pointer.Err
	}
	return ""
}

func withSlackRateLimitRetry[T any](ctx context.Context, s *ContextSyncer, operation string, call func() (T, error)) (T, error) {
	var zero T
	for attempt := 1; ; attempt++ {
		if err := s.waitForSlackMethod(ctx, operation); err != nil {
			return zero, err
		}
		value, err := call()
		if err == nil {
			return value, nil
		}
		var rateLimited *slackapi.RateLimitedError
		if !errors.As(err, &rateLimited) {
			return zero, err
		}
		retryAfter := rateLimited.RetryAfter
		if retryAfter <= 0 {
			retryAfter = time.Second
		}
		s.options.Logger.WithCtx(blackbox.Ctx{
			"operation":      operation,
			"attempt":        attempt,
			"retry_after_ms": retryAfter.Milliseconds(),
		}).Warn("Slack context sync rate limited; waiting before retry")
		timer := time.NewTimer(retryAfter)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return zero, ctx.Err()
		case <-timer.C:
		}
	}
}

func (s *ContextSyncer) waitForSlackMethod(ctx context.Context, operation string) error {
	if s.options.RequestInterval <= 0 || (operation != "conversations.history" && operation != "conversations.replies") {
		return nil
	}
	s.paceMu.Lock()
	now := time.Now()
	next := s.nextRequest[operation]
	wait := next.Sub(now)
	if wait < 0 {
		wait = 0
	}
	base := now
	if next.After(base) {
		base = next
	}
	s.nextRequest[operation] = base.Add(s.options.RequestInterval)
	s.paceMu.Unlock()
	if wait == 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *ContextSyncer) historyEnvelope(channelID string, restricted bool, message slackapi.Message) types.SlackEnvelope {
	eventTime := slackTimestamp(message.Timestamp)
	return types.SlackEnvelope{
		OrganizationID: s.options.OrganizationID,
		EnvelopeID:     "history/" + s.options.TeamID + "/" + channelID + "/" + message.Timestamp,
		EventID:        canonicalMessageEventID("", s.options.TeamID, channelID, message.Timestamp),
		TeamID:         s.options.TeamID,
		ChannelID:      channelID,
		MessageTS:      message.Timestamp,
		ThreadTS:       message.ThreadTimestamp,
		UserID:         message.User,
		BotID:          message.BotID,
		Kind:           types.SlackEventMessage,
		Subtype:        message.SubType,
		Text:           message.Text,
		EventTime:      eventTime,
		ReceivedAt:     time.Now().UTC(),
		IsMention:      mentionsSlackUser(message.Text, s.options.BotUserID),
		Restricted:     restricted,
	}
}

func mentionsSlackUser(text, userID string) bool {
	if userID == "" {
		return false
	}
	prefix := "<@" + userID
	return strings.Contains(text, prefix+">") || strings.Contains(text, prefix+"|")
}

func contextChannel(organizationID, teamID string, channel slackapi.Channel, botMembership map[string]bool) types.SlackContextChannel {
	name := channel.Name
	if name == "" && channel.IsIM {
		name = channel.User
	}
	isChannel := channel.IsChannel || channel.IsGroup
	return types.SlackContextChannel{
		OrganizationID:     organizationID,
		TeamID:             teamID,
		ChannelID:          channel.ID,
		Name:               name,
		Restricted:         channelRestricted(channel),
		RestrictionKnown:   true,
		IsChannel:          isChannel,
		BotIsMember:        isChannel && botMembership[channel.ID],
		BotMembershipKnown: isChannel,
	}
}

func channelRestricted(channel slackapi.Channel) bool {
	return channel.IsPrivate || channel.IsGroup || channel.IsIM || channel.IsMpIM
}

func slackHistoryTimestamp(value time.Time) string {
	value = value.UTC()
	return strconv.FormatInt(value.Unix(), 10) + "." + fmt.Sprintf("%06d", value.Nanosecond()/1_000)
}

func historyMessageEligible(message slackapi.Message, after, through time.Time) bool {
	if message.Timestamp == "" || message.Hidden || message.SubType == slackapi.MsgSubTypeMessageChanged || message.SubType == slackapi.MsgSubTypeMessageDeleted {
		return false
	}
	at := slackTimestamp(message.Timestamp)
	return at.After(after) && !at.After(through)
}
