package slack

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/telemetryos/tos-tag/core/database"
	"github.com/telemetryos/tos-tag/models"
)

// ContextSyncStateStore owns durable, content-free Slack history progress.
// It never stores message text or widens conversation authorization.
type ContextSyncStateStore interface {
	List(context.Context, string, string) (map[string]models.SlackContextSyncState, error)
	Advance(context.Context, string, string, string, time.Time) error
	BeginCatchUp(context.Context, string, string, string, time.Time) error
	CheckpointCatchUp(context.Context, string, string, string, time.Time, time.Time) error
	CheckpointThreadCatchUp(context.Context, string, string, string, time.Time, string, time.Time) error
	CompleteCatchUp(context.Context, string, string, string, time.Time) error
	CompleteBootstrap(context.Context, string, string, string, time.Time) error
}

type MongoContextSyncStateStore struct {
	db  *database.Database
	now func() time.Time
}

func NewMongoContextSyncStateStore(db *database.Database) *MongoContextSyncStateStore {
	return &MongoContextSyncStateStore{db: db, now: time.Now}
}

func (s *MongoContextSyncStateStore) List(ctx context.Context, organizationID, teamID string) (map[string]models.SlackContextSyncState, error) {
	states := make(map[string]models.SlackContextSyncState)
	cursor, err := s.db.Collection(models.CollectionSlackContextSync).Find(ctx, bson.M{"organization_id": organizationID, "team_id": teamID})
	if err != nil {
		return nil, fmt.Errorf("list Slack context sync states: %w", err)
	}
	defer cursor.Close(ctx)
	for cursor.Next(ctx) {
		var state models.SlackContextSyncState
		if err := cursor.Decode(&state); err != nil {
			return nil, fmt.Errorf("decode Slack context sync state: %w", err)
		}
		states[state.ChannelID] = state
	}
	if err := cursor.Err(); err != nil {
		return nil, fmt.Errorf("iterate Slack context sync states: %w", err)
	}
	return states, nil
}

func (s *MongoContextSyncStateStore) Advance(ctx context.Context, organizationID, teamID, channelID string, through time.Time) error {
	if organizationID == "" || teamID == "" || channelID == "" || through.IsZero() {
		return fmt.Errorf("invalid Slack context sync advancement")
	}
	now := s.now().UTC()
	active, err := s.db.Collection(models.CollectionSlackContextSync).UpdateOne(ctx,
		bson.M{"organization_id": organizationID, "team_id": teamID, "channel_id": channelID, "catch_up_through": bson.M{"$exists": true, "$ne": time.Time{}}},
		bson.M{"$max": bson.M{"live_through": through.UTC()}, "$set": bson.M{"updated_at": now}},
	)
	if err != nil {
		return fmt.Errorf("advance live Slack context sync state: %w", err)
	}
	if active.MatchedCount > 0 {
		return nil
	}
	_, err = s.db.Collection(models.CollectionSlackContextSync).UpdateOne(ctx,
		bson.M{"organization_id": organizationID, "team_id": teamID, "channel_id": channelID},
		bson.M{
			"$setOnInsert": bson.M{"organization_id": organizationID, "team_id": teamID, "channel_id": channelID, "bootstrap_completed": false},
			"$max":         bson.M{"synced_through": through.UTC()},
			"$set":         bson.M{"updated_at": now},
		},
		options.UpdateOne().SetUpsert(true),
	)
	if err != nil {
		return fmt.Errorf("advance Slack context sync state: %w", err)
	}
	return nil
}

func (s *MongoContextSyncStateStore) BeginCatchUp(ctx context.Context, organizationID, teamID, channelID string, through time.Time) error {
	if organizationID == "" || teamID == "" || channelID == "" || through.IsZero() {
		return fmt.Errorf("invalid Slack context catch-up start")
	}
	_, err := s.db.Collection(models.CollectionSlackContextSync).UpdateOne(ctx,
		bson.M{
			"organization_id": organizationID, "team_id": teamID, "channel_id": channelID,
			"bootstrap_completed": true,
			"$or": bson.A{
				bson.M{"catch_up_through": bson.M{"$exists": false}},
				bson.M{"catch_up_through": time.Time{}},
				bson.M{"catch_up_through": bson.M{"$lt": through.UTC()}},
			},
		},
		bson.M{"$set": bson.M{"catch_up_through": through.UTC(), "catch_up_latest": through.UTC(), "catch_up_threads": bson.A{}, "updated_at": s.now().UTC()}},
	)
	if err != nil {
		return fmt.Errorf("begin Slack context catch-up: %w", err)
	}
	return nil
}

func (s *MongoContextSyncStateStore) CheckpointCatchUp(ctx context.Context, organizationID, teamID, channelID string, through, latest time.Time) error {
	if organizationID == "" || teamID == "" || channelID == "" || through.IsZero() || latest.IsZero() || !latest.Before(through) {
		return fmt.Errorf("invalid Slack context catch-up checkpoint")
	}
	result, err := s.db.Collection(models.CollectionSlackContextSync).UpdateOne(ctx,
		bson.M{"organization_id": organizationID, "team_id": teamID, "channel_id": channelID, "catch_up_through": through.UTC()},
		bson.M{"$set": bson.M{"catch_up_latest": latest.UTC(), "updated_at": s.now().UTC()}},
	)
	if err != nil {
		return fmt.Errorf("checkpoint Slack context catch-up: %w", err)
	}
	if result.MatchedCount == 0 {
		return fmt.Errorf("Slack context catch-up checkpoint is stale")
	}
	return nil
}

func (s *MongoContextSyncStateStore) CheckpointThreadCatchUp(ctx context.Context, organizationID, teamID, channelID string, through time.Time, rootThreadTS string, syncedThrough time.Time) error {
	if organizationID == "" || teamID == "" || channelID == "" || through.IsZero() || rootThreadTS == "" || syncedThrough.IsZero() {
		return fmt.Errorf("invalid Slack thread catch-up checkpoint")
	}
	var state models.SlackContextSyncState
	filter := bson.M{"organization_id": organizationID, "team_id": teamID, "channel_id": channelID, "catch_up_through": through.UTC()}
	if err := s.db.Collection(models.CollectionSlackContextSync).FindOne(ctx, filter).Decode(&state); err != nil {
		return fmt.Errorf("load Slack thread catch-up checkpoint: %w", err)
	}
	found := false
	for index := range state.CatchUpThreads {
		if state.CatchUpThreads[index].RootThreadTS == rootThreadTS {
			if syncedThrough.After(state.CatchUpThreads[index].SyncedThrough) {
				state.CatchUpThreads[index].SyncedThrough = syncedThrough.UTC()
			}
			found = true
			break
		}
	}
	if !found {
		state.CatchUpThreads = append(state.CatchUpThreads, models.SlackThreadCatchUpState{RootThreadTS: rootThreadTS, SyncedThrough: syncedThrough.UTC()})
	}
	result, err := s.db.Collection(models.CollectionSlackContextSync).UpdateOne(ctx, filter, bson.M{"$set": bson.M{"catch_up_threads": state.CatchUpThreads, "updated_at": s.now().UTC()}})
	if err != nil {
		return fmt.Errorf("checkpoint Slack thread catch-up: %w", err)
	}
	if result.MatchedCount == 0 {
		return fmt.Errorf("Slack thread catch-up checkpoint is stale")
	}
	return nil
}

func (s *MongoContextSyncStateStore) CompleteCatchUp(ctx context.Context, organizationID, teamID, channelID string, through time.Time) error {
	if organizationID == "" || teamID == "" || channelID == "" || through.IsZero() {
		return fmt.Errorf("invalid Slack context catch-up completion")
	}
	var state models.SlackContextSyncState
	if err := s.db.Collection(models.CollectionSlackContextSync).FindOne(ctx, bson.M{
		"organization_id": organizationID, "team_id": teamID, "channel_id": channelID, "catch_up_through": through.UTC(),
	}).Decode(&state); err != nil {
		return fmt.Errorf("load Slack context catch-up completion: %w", err)
	}
	completedThrough := through.UTC()
	if state.LiveThrough.After(completedThrough) {
		completedThrough = state.LiveThrough.UTC()
	}
	result, err := s.db.Collection(models.CollectionSlackContextSync).UpdateOne(ctx,
		bson.M{"organization_id": organizationID, "team_id": teamID, "channel_id": channelID, "catch_up_through": through.UTC()},
		bson.M{
			"$max":   bson.M{"synced_through": completedThrough},
			"$unset": bson.M{"catch_up_through": "", "catch_up_latest": "", "catch_up_threads": "", "live_through": ""},
			"$set":   bson.M{"updated_at": s.now().UTC()},
		},
	)
	if err != nil {
		return fmt.Errorf("complete Slack context catch-up: %w", err)
	}
	if result.MatchedCount == 0 {
		return fmt.Errorf("Slack context catch-up completion is stale")
	}
	return nil
}

func (s *MongoContextSyncStateStore) CompleteBootstrap(ctx context.Context, organizationID, teamID, channelID string, through time.Time) error {
	if organizationID == "" || teamID == "" || channelID == "" || through.IsZero() {
		return fmt.Errorf("invalid Slack context bootstrap completion")
	}
	now := s.now().UTC()
	_, err := s.db.Collection(models.CollectionSlackContextSync).UpdateOne(ctx,
		bson.M{"organization_id": organizationID, "team_id": teamID, "channel_id": channelID},
		bson.M{
			"$setOnInsert": bson.M{"organization_id": organizationID, "team_id": teamID, "channel_id": channelID},
			"$max":         bson.M{"synced_through": through.UTC()},
			"$set":         bson.M{"bootstrap_completed": true, "bootstrap_completed_at": now, "updated_at": now},
		},
		options.UpdateOne().SetUpsert(true),
	)
	if err != nil {
		return fmt.Errorf("complete Slack context bootstrap: %w", err)
	}
	return nil
}

type MemoryContextSyncStateStore struct {
	mu     sync.Mutex
	states map[string]models.SlackContextSyncState
}

func NewMemoryContextSyncStateStore() *MemoryContextSyncStateStore {
	return &MemoryContextSyncStateStore{states: make(map[string]models.SlackContextSyncState)}
}

func contextSyncStateKey(organizationID, teamID, channelID string) string {
	return organizationID + "/" + teamID + "/" + channelID
}

func (s *MemoryContextSyncStateStore) List(_ context.Context, organizationID, teamID string) (map[string]models.SlackContextSyncState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make(map[string]models.SlackContextSyncState)
	for _, state := range s.states {
		if state.OrganizationID == organizationID && state.TeamID == teamID {
			result[state.ChannelID] = state
		}
	}
	return result, nil
}

func (s *MemoryContextSyncStateStore) Advance(_ context.Context, organizationID, teamID, channelID string, through time.Time) error {
	if organizationID == "" || teamID == "" || channelID == "" || through.IsZero() {
		return fmt.Errorf("invalid Slack context sync advancement")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := contextSyncStateKey(organizationID, teamID, channelID)
	state := s.states[key]
	state.OrganizationID, state.TeamID, state.ChannelID = organizationID, teamID, channelID
	if !state.CatchUpThrough.IsZero() && through.After(state.LiveThrough) {
		state.LiveThrough = through.UTC()
	} else if state.CatchUpThrough.IsZero() && through.After(state.SyncedThrough) {
		state.SyncedThrough = through.UTC()
	}
	state.UpdatedAt = time.Now().UTC()
	s.states[key] = state
	return nil
}

func (s *MemoryContextSyncStateStore) BeginCatchUp(_ context.Context, organizationID, teamID, channelID string, through time.Time) error {
	if organizationID == "" || teamID == "" || channelID == "" || through.IsZero() {
		return fmt.Errorf("invalid Slack context catch-up start")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := contextSyncStateKey(organizationID, teamID, channelID)
	state := s.states[key]
	if !state.BootstrapCompleted || (!state.CatchUpThrough.IsZero() && !through.After(state.CatchUpThrough)) {
		return nil
	}
	state.OrganizationID, state.TeamID, state.ChannelID = organizationID, teamID, channelID
	state.CatchUpThrough = through.UTC()
	state.CatchUpLatest = through.UTC()
	state.CatchUpThreads = nil
	state.UpdatedAt = time.Now().UTC()
	s.states[key] = state
	return nil
}

func (s *MemoryContextSyncStateStore) CheckpointCatchUp(_ context.Context, organizationID, teamID, channelID string, through, latest time.Time) error {
	if organizationID == "" || teamID == "" || channelID == "" || through.IsZero() || latest.IsZero() || !latest.Before(through) {
		return fmt.Errorf("invalid Slack context catch-up checkpoint")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := contextSyncStateKey(organizationID, teamID, channelID)
	state := s.states[key]
	if !state.CatchUpThrough.Equal(through.UTC()) {
		return fmt.Errorf("Slack context catch-up checkpoint is stale")
	}
	state.CatchUpLatest = latest.UTC()
	state.UpdatedAt = time.Now().UTC()
	s.states[key] = state
	return nil
}

func (s *MemoryContextSyncStateStore) CheckpointThreadCatchUp(_ context.Context, organizationID, teamID, channelID string, through time.Time, rootThreadTS string, syncedThrough time.Time) error {
	if organizationID == "" || teamID == "" || channelID == "" || through.IsZero() || rootThreadTS == "" || syncedThrough.IsZero() {
		return fmt.Errorf("invalid Slack thread catch-up checkpoint")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := contextSyncStateKey(organizationID, teamID, channelID)
	state := s.states[key]
	if !state.CatchUpThrough.Equal(through.UTC()) {
		return fmt.Errorf("Slack thread catch-up checkpoint is stale")
	}
	found := false
	for index := range state.CatchUpThreads {
		if state.CatchUpThreads[index].RootThreadTS == rootThreadTS {
			if syncedThrough.After(state.CatchUpThreads[index].SyncedThrough) {
				state.CatchUpThreads[index].SyncedThrough = syncedThrough.UTC()
			}
			found = true
			break
		}
	}
	if !found {
		state.CatchUpThreads = append(state.CatchUpThreads, models.SlackThreadCatchUpState{RootThreadTS: rootThreadTS, SyncedThrough: syncedThrough.UTC()})
	}
	state.UpdatedAt = time.Now().UTC()
	s.states[key] = state
	return nil
}

func (s *MemoryContextSyncStateStore) CompleteCatchUp(_ context.Context, organizationID, teamID, channelID string, through time.Time) error {
	if organizationID == "" || teamID == "" || channelID == "" || through.IsZero() {
		return fmt.Errorf("invalid Slack context catch-up completion")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := contextSyncStateKey(organizationID, teamID, channelID)
	state := s.states[key]
	if !state.CatchUpThrough.Equal(through.UTC()) {
		return fmt.Errorf("Slack context catch-up completion is stale")
	}
	completedThrough := through.UTC()
	if state.LiveThrough.After(completedThrough) {
		completedThrough = state.LiveThrough.UTC()
	}
	if completedThrough.After(state.SyncedThrough) {
		state.SyncedThrough = completedThrough
	}
	state.CatchUpThrough = time.Time{}
	state.CatchUpLatest = time.Time{}
	state.CatchUpThreads = nil
	state.LiveThrough = time.Time{}
	state.UpdatedAt = time.Now().UTC()
	s.states[key] = state
	return nil
}

func (s *MemoryContextSyncStateStore) CompleteBootstrap(_ context.Context, organizationID, teamID, channelID string, through time.Time) error {
	if err := s.Advance(context.Background(), organizationID, teamID, channelID, through); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := contextSyncStateKey(organizationID, teamID, channelID)
	state := s.states[key]
	state.BootstrapCompleted = true
	state.BootstrapCompletedAt = time.Now().UTC()
	state.UpdatedAt = state.BootstrapCompletedAt
	s.states[key] = state
	return nil
}
