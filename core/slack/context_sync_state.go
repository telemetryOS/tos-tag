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
	_, err := s.db.Collection(models.CollectionSlackContextSync).UpdateOne(ctx,
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
	if through.After(state.SyncedThrough) {
		state.SyncedThrough = through.UTC()
	}
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
