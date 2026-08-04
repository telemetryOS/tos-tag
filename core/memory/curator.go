package memory

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/RobertWHurst/blackbox"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/telemetryos/tos-tag/core/database"
	"github.com/telemetryos/tos-tag/core/usage"
	"github.com/telemetryos/tos-tag/models"
	"github.com/telemetryos/tos-tag/types"
)

type CuratorOptions struct {
	Interval        time.Duration
	Lookback        time.Duration
	MinMessages     int
	MaxMessages     int
	MaxScopesPerRun int
	MinConfidence   float64
	Timeout         time.Duration
}

type Curator struct {
	db         *database.Database
	repository Repository
	summarizer Summarizer
	usage      usage.Recorder
	logger     *blackbox.Logger
	options    CuratorOptions
	now        func() time.Time
	mu         sync.Mutex
	cancel     context.CancelFunc
	done       chan struct{}
}

func NewCurator(db *database.Database, repository Repository, summarizer Summarizer, recorder usage.Recorder, logger *blackbox.Logger, options CuratorOptions) (*Curator, error) {
	if db == nil || repository == nil || summarizer == nil || options.Interval <= 0 || options.Lookback <= 0 || options.MinMessages < 2 || options.MaxMessages < options.MinMessages || options.MaxScopesPerRun <= 0 || options.MinConfidence < 0 || options.MinConfidence > 1 || options.Timeout <= 0 {
		return nil, errors.New("invalid memory curator configuration")
	}
	if logger == nil {
		logger = blackbox.New()
	}
	return &Curator{db: db, repository: repository, summarizer: summarizer, usage: recorder, logger: logger, options: options, now: time.Now}, nil
}

func (c *Curator) Start(parent context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(parent)
	c.cancel = cancel
	c.done = make(chan struct{})
	go func() {
		defer close(c.done)
		timer := time.NewTimer(5 * time.Second)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				if _, err := c.RunOnce(ctx); err != nil && ctx.Err() == nil {
					c.logger.WithCtx(blackbox.Ctx{"error_type": fmt.Sprintf("%T", err)}).Warn("memory curation pass failed")
				}
				timer.Reset(c.options.Interval)
			}
		}
	}()
}

func (c *Curator) Stop(ctx context.Context) error {
	c.mu.Lock()
	cancel, done := c.cancel, c.done
	c.cancel, c.done = nil, nil
	c.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Curator) RunOnce(ctx context.Context) (int, error) {
	batches, err := c.candidates(ctx)
	if err != nil {
		return 0, err
	}
	updated := 0
	for _, batch := range batches {
		if ctx.Err() != nil {
			return updated, ctx.Err()
		}
		current, err := c.repository.FindScope(ctx, batch.OrganizationID, batch.ScopeKey)
		if err == nil && (current.SourceHash == batch.SourceHash || current.Pinned || current.Origin == "operator") {
			continue
		}
		if err != nil && !errors.Is(err, ErrNotFound) {
			return updated, err
		}
		callCtx, cancel := context.WithTimeout(ctx, c.options.Timeout)
		started := time.Now()
		result, err := c.summarizer.Summarize(callCtx, batch)
		cancel()
		duration := time.Since(started)
		if c.usage != nil {
			_ = c.usage.Record(ctx, usage.Event{OrganizationID: batch.OrganizationID, Category: "memory_curation", ProviderID: "openai", ModelID: c.summarizer.Model(), InputTokens: result.InputTokens, OutputTokens: result.OutputTokens, Calls: 1, DurationMS: duration.Milliseconds()})
		}
		if err != nil {
			c.logger.WithCtx(blackbox.Ctx{"organization_id": batch.OrganizationID, "channel_id": batch.ChannelID, "scope": batch.Scope, "model": c.summarizer.Model(), "reasoning_effort": c.summarizer.Effort(), "duration_ms": duration.Milliseconds(), "error_type": fmt.Sprintf("%T", err)}).Warn("memory summary request failed")
			continue
		}
		if strings.TrimSpace(result.Summary) == "" || result.Confidence < c.options.MinConfidence {
			continue
		}
		sourceSet := make(map[string]SourceMessage, len(batch.Messages))
		naturalExpiry := time.Time{}
		for _, source := range batch.Messages {
			sourceSet[source.ID] = source
			if naturalExpiry.IsZero() || source.ExpiresAt.Before(naturalExpiry) {
				naturalExpiry = source.ExpiresAt
			}
		}
		facts := make([]Fact, 0, len(result.Facts))
		for _, candidate := range result.Facts {
			if candidate.Confidence < c.options.MinConfidence || strings.TrimSpace(candidate.Text) == "" {
				continue
			}
			valid := true
			for _, id := range candidate.SourceIDs {
				if _, ok := sourceSet[id]; !ok {
					valid = false
					break
				}
			}
			if !valid || len(candidate.SourceIDs) == 0 {
				continue
			}
			expires := c.now().UTC().Add(time.Duration(candidate.ValidForHours) * time.Hour)
			if !naturalExpiry.IsZero() && naturalExpiry.Before(expires) {
				expires = naturalExpiry
			}
			facts = append(facts, Fact{Text: strings.TrimSpace(candidate.Text), Confidence: candidate.Confidence, SourceIDs: append([]string(nil), candidate.SourceIDs...), ExpiresAt: expires})
		}
		// A fluent summary is not, by itself, durable memory. Requiring at least
		// one source-linked fact prevents empty-channel boilerplate from becoming
		// recallable context merely because the model assigned it high confidence.
		if len(facts) == 0 {
			continue
		}
		sourceIDs := make([]string, 0, len(batch.Messages))
		for _, source := range batch.Messages {
			sourceIDs = append(sourceIDs, source.ID)
		}
		record := Record{OrganizationID: batch.OrganizationID, ChannelID: batch.ChannelID, RootThreadTS: batch.RootThreadTS, Scope: batch.Scope, ScopeKey: batch.ScopeKey, Restricted: batch.Restricted, Text: result.Summary, Facts: facts, Confidence: result.Confidence, SourceIDs: sourceIDs, SourceHash: batch.SourceHash, Model: c.summarizer.Model(), ReasoningEffort: c.summarizer.Effort(), Status: StatusActive, NaturalExpiresAt: naturalExpiry, ExpiresAt: naturalExpiry}
		if _, changed, err := c.repository.PutGenerated(ctx, record); err != nil {
			return updated, err
		} else if changed {
			updated++
			c.logger.WithCtx(blackbox.Ctx{"organization_id": batch.OrganizationID, "channel_id": batch.ChannelID, "scope": batch.Scope, "source_count": len(batch.Messages), "fact_count": len(facts), "model": c.summarizer.Model(), "reasoning_effort": c.summarizer.Effort(), "input_tokens": result.InputTokens, "output_tokens": result.OutputTokens, "duration_ms": duration.Milliseconds()}).Info("durable memory updated")
		}
	}
	return updated, nil
}

func (c *Curator) candidates(ctx context.Context) ([]Batch, error) {
	now := c.now().UTC()
	excluded := make(map[string]struct{})
	channelCursor, err := c.db.Collection(models.CollectionChannels).Find(ctx, bson.M{"context_history_mode": string(types.ContextHistorySessionOnly)})
	if err != nil {
		return nil, err
	}
	var sessionOnlyChannels []models.Channel
	if err := channelCursor.All(ctx, &sessionOnlyChannels); err != nil {
		_ = channelCursor.Close(ctx)
		return nil, err
	}
	_ = channelCursor.Close(ctx)
	for _, channel := range sessionOnlyChannels {
		excluded[channel.OrganizationID+"/"+channel.ChannelID] = struct{}{}
	}
	cursor, err := c.db.Collection(models.CollectionMessages).Find(ctx, bson.M{
		"deleted":     false,
		"bot_id":      bson.M{"$in": bson.A{"", nil}},
		"subtype":     bson.M{"$nin": bson.A{types.SlackMessageSubtypeBotMessage, types.SlackMessageSubtypeAssistantAppThread}},
		"text":        bson.M{"$ne": ""},
		"original_at": bson.M{"$gte": now.Add(-c.options.Lookback)},
		"expires_at":  bson.M{"$gt": now},
	}, options.Find().SetSort(bson.D{{Key: "updated_at", Value: -1}}).SetLimit(5000))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var messages []models.ChannelMessage
	if err := cursor.All(ctx, &messages); err != nil {
		return nil, err
	}
	type group struct {
		organizationID string
		channelID      string
		rootThreadTS   string
		scope          Scope
		restricted     bool
		messages       []models.ChannelMessage
	}
	groups := make(map[string]*group)
	byMessage := make(map[string]models.ChannelMessage, len(messages))
	for _, message := range messages {
		if _, skip := excluded[message.OrganizationID+"/"+message.ChannelID]; skip {
			continue
		}
		byMessage[message.OrganizationID+"/"+message.ChannelID+"/"+message.MessageTS] = message
		key := message.OrganizationID + "/" + message.ChannelID + "/channel"
		if groups[key] == nil {
			groups[key] = &group{organizationID: message.OrganizationID, channelID: message.ChannelID, scope: ScopeChannel, restricted: message.Restricted}
		}
		groups[key].messages = append(groups[key].messages, message)
		if message.RootThreadTS != "" && message.RootThreadTS != message.MessageTS {
			threadKey := message.OrganizationID + "/" + message.ChannelID + "/thread/" + message.RootThreadTS
			if groups[threadKey] == nil {
				groups[threadKey] = &group{organizationID: message.OrganizationID, channelID: message.ChannelID, rootThreadTS: message.RootThreadTS, scope: ScopeThread, restricted: message.Restricted}
			}
			groups[threadKey].messages = append(groups[threadKey].messages, message)
		}
	}
	for _, value := range groups {
		if value.scope != ScopeThread {
			continue
		}
		root, ok := byMessage[value.organizationID+"/"+value.channelID+"/"+value.rootThreadTS]
		if ok {
			value.messages = append(value.messages, root)
		}
	}
	var batches []Batch
	for key, value := range groups {
		if len(value.messages) < c.options.MinMessages {
			continue
		}
		sort.Slice(value.messages, func(i, j int) bool { return value.messages[i].OriginalAt.Before(value.messages[j].OriginalAt) })
		if len(value.messages) > c.options.MaxMessages {
			value.messages = value.messages[len(value.messages)-c.options.MaxMessages:]
		}
		batch := Batch{OrganizationID: value.organizationID, ChannelID: value.channelID, RootThreadTS: value.rootThreadTS, Scope: value.scope, ScopeKey: key, Restricted: value.restricted}
		hash := sha256.New()
		for _, message := range value.messages {
			id := message.ChannelID + "/" + message.MessageTS
			batch.Messages = append(batch.Messages, SourceMessage{ID: id, AuthorID: message.AuthorID, Text: message.Text, ObservedAt: message.OriginalAt, ExpiresAt: message.ExpiresAt})
			_, _ = fmt.Fprintf(hash, "%s\x00%d\x00%s\x00", id, message.ProjectionVersion, message.Text)
		}
		batch.SourceHash = hex.EncodeToString(hash.Sum(nil))
		batches = append(batches, batch)
	}
	sort.Slice(batches, func(i, j int) bool {
		left := batches[i].Messages[len(batches[i].Messages)-1].ObservedAt
		right := batches[j].Messages[len(batches[j].Messages)-1].ObservedAt
		return left.After(right)
	})
	if len(batches) > c.options.MaxScopesPerRun {
		batches = batches[:c.options.MaxScopesPerRun]
	}
	return batches, nil
}
