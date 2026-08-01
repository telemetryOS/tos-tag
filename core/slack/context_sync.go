package slack

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
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
	BotUserID          string
	Lookback           time.Duration
	Timeout            time.Duration
	MaxChannels        int
	MaxMessages        int
	MessagesPerChannel int
	Logger             *blackbox.Logger
}

type ContextSyncStats struct {
	ChannelsDiscovered int
	ChannelsRegistered int
	ChannelsSkipped    int
	MessagesImported   int
	StartedAt          time.Time
	CompletedAt        time.Time
}

// ContextSyncRun is the immutable result of channel discovery and policy
// registration. Its channel inventory stays private to the Slack adapter so
// callers cannot use the User OAuth client for unrelated operations.
type ContextSyncRun struct {
	stats    ContextSyncStats
	channels []slackapi.Channel
	cutoff   time.Time
}

type ContextChannelHandler func(context.Context, types.SlackContextChannel) (bool, error)
type ContextMessageHandler func(context.Context, types.SlackEnvelope) error

type contextSyncAPI interface {
	GetConversationsForUserContext(context.Context, *slackapi.GetConversationsForUserParameters) ([]slackapi.Channel, string, error)
	GetConversationHistoryContext(context.Context, *slackapi.GetConversationHistoryParameters) (*slackapi.GetConversationHistoryResponse, error)
	GetConversationRepliesContext(context.Context, *slackapi.GetConversationRepliesParameters) ([]slackapi.Message, bool, string, error)
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
	options ContextSyncOptions
	api     contextSyncAPI
}

func NewContextSyncer(options ContextSyncOptions) (*ContextSyncer, error) {
	if !strings.HasPrefix(options.UserOAuthToken, "xoxp-") {
		return nil, errors.New("Slack context sync requires a User OAuth xoxp token")
	}
	api := slackapi.New(options.UserOAuthToken)
	return newContextSyncer(options, api)
}

func newContextSyncer(options ContextSyncOptions, api contextSyncAPI) (*ContextSyncer, error) {
	if options.OrganizationID == "" || options.TeamID == "" || options.Lookback <= 0 || options.Timeout <= 0 || options.MaxChannels <= 0 || options.MaxMessages <= 0 || options.MessagesPerChannel <= 0 || api == nil {
		return nil, errors.New("invalid Slack context sync options")
	}
	if options.Logger == nil {
		options.Logger = blackbox.New()
	}
	return &ContextSyncer{options: options, api: api}, nil
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
	run := &ContextSyncRun{stats: stats, cutoff: time.Now().UTC().Add(-s.options.Lookback)}
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
	run.stats.ChannelsDiscovered = len(channels)
	registeredChannels := make([]slackapi.Channel, 0, len(channels))
	for _, channel := range channels {
		authorized, err := register(ctx, contextChannel(s.options.OrganizationID, s.options.TeamID, channel))
		if err != nil {
			return run, fmt.Errorf("register Slack context channel %s: %w", channel.ID, err)
		}
		if authorized {
			registeredChannels = append(registeredChannels, channel)
			run.stats.ChannelsRegistered++
		}
	}
	run.channels = registeredChannels
	s.options.Logger.WithCtx(blackbox.Ctx{
		"organization_id":     s.options.OrganizationID,
		"channels_discovered": run.stats.ChannelsDiscovered,
		"channels_registered": run.stats.ChannelsRegistered,
		"duration_ms":         time.Since(run.stats.StartedAt).Milliseconds(),
	}).Info("Slack user context discovery completed")
	return run, nil
}

// Backfill imports a bounded, idempotent history snapshot for a previously
// authorized discovery run. It may run concurrently with live Socket Mode
// ingestion because both paths share canonical event IDs.
func (s *ContextSyncer) Backfill(parent context.Context, run *ContextSyncRun, importMessage ContextMessageHandler) (ContextSyncStats, error) {
	if run == nil || importMessage == nil {
		return ContextSyncStats{}, errors.New("Slack context backfill run and message handler are required")
	}
	ctx, cancel := context.WithTimeout(parent, s.options.Timeout)
	defer cancel()
	stats := run.stats

	remaining := s.options.MaxMessages
	for index, channel := range run.channels {
		if remaining <= 0 {
			break
		}
		channelsLeft := len(run.channels) - index
		fairShare := (remaining + channelsLeft - 1) / channelsLeft
		budget := min(s.options.MessagesPerChannel, fairShare)
		imported, err := s.backfillChannel(ctx, channel, run.cutoff, budget, importMessage)
		if err != nil {
			if code, recoverable := recoverableContextChannelError(err); recoverable {
				stats.ChannelsSkipped++
				s.options.Logger.WithCtx(blackbox.Ctx{
					"organization_id": s.options.OrganizationID,
					"channel_id":      channel.ID,
					"error_code":      code,
				}).Warn("Slack context channel became inaccessible during backfill; skipping")
				continue
			}
			return stats, fmt.Errorf("backfill Slack context channel %s: %w", channel.ID, err)
		}
		stats.MessagesImported += imported
		remaining -= imported
	}
	stats.CompletedAt = time.Now().UTC()
	s.options.Logger.WithCtx(blackbox.Ctx{
		"organization_id":     s.options.OrganizationID,
		"channels_discovered": stats.ChannelsDiscovered,
		"channels_registered": stats.ChannelsRegistered,
		"channels_skipped":    stats.ChannelsSkipped,
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
		result, err := withSlackRateLimitRetry(ctx, s.options.Logger, "users.conversations", func() (contextChannelPage, error) {
			page, next, callErr := s.api.GetConversationsForUserContext(ctx, &slackapi.GetConversationsForUserParameters{
				Cursor: cursor, Types: []string{"public_channel", "private_channel", "mpim", "im"},
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

func (s *ContextSyncer) backfillChannel(ctx context.Context, channel slackapi.Channel, cutoff time.Time, budget int, importMessage ContextMessageHandler) (int, error) {
	if budget <= 0 {
		return 0, nil
	}
	restricted := channelRestricted(channel)
	oldest := strconv.FormatInt(cutoff.Unix(), 10)
	rootBudget := budget
	if budget >= 4 {
		rootBudget = budget * 3 / 4
	}
	var roots []slackapi.Message
	imported := 0
	cursor := ""
	for imported < rootBudget {
		pageLimit := min(contextHistoryPageSize, rootBudget-imported)
		page, err := withSlackRateLimitRetry(ctx, s.options.Logger, "conversations.history", func() (*slackapi.GetConversationHistoryResponse, error) {
			return s.api.GetConversationHistoryContext(ctx, &slackapi.GetConversationHistoryParameters{
				ChannelID: channel.ID, Cursor: cursor, Limit: pageLimit, Oldest: oldest,
			})
		})
		if err != nil {
			return imported, err
		}
		for _, message := range page.Messages {
			if imported >= rootBudget {
				break
			}
			if !historyMessageEligible(message, cutoff) {
				continue
			}
			if err := importMessage(ctx, s.historyEnvelope(channel.ID, restricted, message)); err != nil {
				return imported, err
			}
			roots = append(roots, message)
			imported++
		}
		cursor = page.ResponseMetaData.NextCursor
		if !page.HasMore || cursor == "" || len(page.Messages) == 0 {
			break
		}
	}

	for _, root := range roots {
		if imported >= budget {
			break
		}
		if root.ReplyCount <= 0 || root.Timestamp == "" {
			continue
		}
		replyCursor := ""
		for imported < budget {
			pageLimit := min(contextHistoryPageSize, budget-imported+1)
			page, err := withSlackRateLimitRetry(ctx, s.options.Logger, "conversations.replies", func() (contextReplyPage, error) {
				replies, hasMore, next, callErr := s.api.GetConversationRepliesContext(ctx, &slackapi.GetConversationRepliesParameters{
					ChannelID: channel.ID, Timestamp: root.Timestamp, Cursor: replyCursor,
					Limit: pageLimit, Oldest: oldest,
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
				return imported, err
			}
			for _, reply := range page.messages {
				if imported >= budget {
					break
				}
				if reply.Timestamp == root.Timestamp || !historyMessageEligible(reply, cutoff) {
					continue
				}
				if reply.ThreadTimestamp == "" {
					reply.ThreadTimestamp = root.Timestamp
				}
				if err := importMessage(ctx, s.historyEnvelope(channel.ID, restricted, reply)); err != nil {
					return imported, err
				}
				imported++
			}
			if !page.hasMore || page.nextCursor == "" || len(page.messages) == 0 {
				break
			}
			replyCursor = page.nextCursor
		}
	}
	return imported, nil
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

func withSlackRateLimitRetry[T any](ctx context.Context, logger *blackbox.Logger, operation string, call func() (T, error)) (T, error) {
	var zero T
	for attempt := 1; ; attempt++ {
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
		logger.WithCtx(blackbox.Ctx{
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
		IsMention:      s.options.BotUserID != "" && strings.Contains(message.Text, "<@"+s.options.BotUserID+">"),
		Restricted:     restricted,
	}
}

func contextChannel(organizationID, teamID string, channel slackapi.Channel) types.SlackContextChannel {
	name := channel.Name
	if name == "" && channel.IsIM {
		name = channel.User
	}
	return types.SlackContextChannel{
		OrganizationID:   organizationID,
		TeamID:           teamID,
		ChannelID:        channel.ID,
		Name:             name,
		Restricted:       channelRestricted(channel),
		RestrictionKnown: true,
	}
}

func channelRestricted(channel slackapi.Channel) bool {
	return channel.IsPrivate || channel.IsGroup || channel.IsIM || channel.IsMpIM
}

func historyMessageEligible(message slackapi.Message, cutoff time.Time) bool {
	if message.Timestamp == "" || message.Hidden || message.SubType == slackapi.MsgSubTypeMessageChanged || message.SubType == slackapi.MsgSubTypeMessageDeleted {
		return false
	}
	return !slackTimestamp(message.Timestamp).Before(cutoff)
}
