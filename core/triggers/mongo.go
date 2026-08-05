package triggers

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/telemetryos/tos-tag/core/database"
	"github.com/telemetryos/tos-tag/core/schedule"
	"github.com/telemetryos/tos-tag/models"
)

type MongoStore struct {
	db  *database.Database
	now func() time.Time
}

func NewMongoStore(db *database.Database) *MongoStore { return &MongoStore{db: db, now: time.Now} }

func (s *MongoStore) PutContext(ctx context.Context, subscription Subscription) (Subscription, error) {
	now := s.now().UTC()
	var err error
	subscription, err = Normalize(subscription, now)
	if err != nil {
		return Subscription{}, err
	}
	after := options.After
	var saved Subscription
	err = s.db.Collection(models.CollectionEventSubscriptions).FindOneAndUpdate(ctx,
		subscriptionScope(subscription.OrganizationID, subscription.WorkspaceID, subscription.ChannelID, subscription.ID),
		bson.M{"$set": bson.M{
			"root_thread_ts": subscription.RootThreadTS, "session_id": subscription.SessionID,
			"generation": subscription.Generation, "owner_id": subscription.OwnerID,
			"kind": subscription.Kind, "instruction": subscription.Instruction,
			"cron": subscription.Cron, "timezone": subscription.Timezone,
			"interval": subscription.Interval, "next_run": subscription.NextRun,
			"classifier_gate": subscription.ClassifierGate, "min_confidence": subscription.MinConfidence,
			"enabled": subscription.Enabled, "updated_at": now,
		}, "$setOnInsert": bson.M{"organization_id": subscription.OrganizationID, "workspace_id": subscription.WorkspaceID, "channel_id": subscription.ChannelID, "public_id": subscription.ID, "created_at": now}, "$inc": bson.M{"version": 1}},
		options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(after)).Decode(&saved)
	if mongo.IsDuplicateKeyError(err) {
		return Subscription{}, ErrScopeConflict
	}
	return saved, err
}

func (s *MongoStore) UpdateContext(ctx context.Context, subscription Subscription, expectedVersion int64) (Subscription, error) {
	now := s.now().UTC()
	var err error
	subscription, err = Normalize(subscription, now)
	if err != nil {
		return Subscription{}, err
	}
	filter := subscriptionScope(subscription.OrganizationID, subscription.WorkspaceID, subscription.ChannelID, subscription.ID)
	filter["version"] = expectedVersion
	var saved Subscription
	err = s.db.Collection(models.CollectionEventSubscriptions).FindOneAndUpdate(ctx, filter, bson.M{"$set": bson.M{
		"root_thread_ts": subscription.RootThreadTS, "session_id": subscription.SessionID,
		"generation": subscription.Generation, "owner_id": subscription.OwnerID,
		"kind": subscription.Kind, "instruction": subscription.Instruction,
		"cron": subscription.Cron, "timezone": subscription.Timezone,
		"interval": subscription.Interval, "next_run": subscription.NextRun,
		"classifier_gate": subscription.ClassifierGate, "min_confidence": subscription.MinConfidence,
		"enabled": subscription.Enabled, "updated_at": now,
	}, "$inc": bson.M{"version": 1}}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&saved)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Subscription{}, ErrUpdateConflict
	}
	return saved, err
}

func (s *MongoStore) GetContext(ctx context.Context, organizationID, workspaceID, channelID, id string) (Subscription, error) {
	var value Subscription
	err := s.db.Collection(models.CollectionEventSubscriptions).FindOne(ctx, subscriptionScope(organizationID, workspaceID, channelID, id)).Decode(&value)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Subscription{}, errors.New("trigger subscription not found")
	}
	return value, err
}

func (s *MongoStore) ListChannel(ctx context.Context, organizationID, workspaceID, channelID string) ([]Subscription, error) {
	return s.find(ctx, bson.M{"organization_id": organizationID, "workspace_id": workspaceID, "channel_id": channelID}, 0)
}

func (s *MongoStore) List(ctx context.Context, organizationID string) ([]Subscription, error) {
	if organizationID == "" {
		return nil, errors.New("organization is required")
	}
	return s.find(ctx, bson.M{"organization_id": organizationID}, 0)
}

func (s *MongoStore) DueContext(ctx context.Context, now time.Time, limit int) ([]Subscription, error) {
	return s.find(ctx, bson.M{"enabled": true, "next_run": bson.M{"$lte": now}}, limit)
}

func (s *MongoStore) find(ctx context.Context, filter bson.M, limit int) ([]Subscription, error) {
	find := options.Find().SetSort(bson.D{{Key: "next_run", Value: 1}, {Key: "public_id", Value: 1}})
	if limit > 0 {
		find.SetLimit(int64(limit))
	}
	cursor, err := s.db.Collection(models.CollectionEventSubscriptions).Find(ctx, filter, find)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	values := make([]Subscription, 0)
	err = cursor.All(ctx, &values)
	return values, err
}

func (s *MongoStore) AdvanceContext(ctx context.Context, organizationID, workspaceID, channelID, id string, from time.Time) error {
	current, err := s.GetContext(ctx, organizationID, workspaceID, channelID, id)
	if err != nil {
		return err
	}
	spec, err := schedule.Parse(current.Cron, current.Timezone, current.Interval)
	if err != nil {
		return err
	}
	next := spec.Advance(current.NextRun, from)
	filter := subscriptionScope(organizationID, workspaceID, channelID, id)
	filter["version"] = current.Version
	result, err := s.db.Collection(models.CollectionEventSubscriptions).UpdateOne(ctx,
		filter,
		bson.M{"$set": bson.M{"next_run": next, "updated_at": s.now().UTC()}, "$inc": bson.M{"version": 1}})
	if err == nil && result.ModifiedCount != 1 {
		return errors.New("trigger subscription advance conflict")
	}
	return err
}

func subscriptionScope(organizationID, workspaceID, channelID, id string) bson.M {
	return bson.M{"organization_id": organizationID, "workspace_id": workspaceID, "channel_id": channelID, "public_id": id}
}

var _ Repository = (*MongoStore)(nil)
