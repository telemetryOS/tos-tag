package routines

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
func (s *MongoStore) PutContext(ctx context.Context, routine Routine) (Routine, error) {
	now := s.now().UTC()
	var err error
	routine, err = normalize(routine, now)
	if err != nil {
		return Routine{}, err
	}
	after := options.After
	var saved Routine
	err = s.db.Collection(models.CollectionRoutines).FindOneAndUpdate(ctx, routineScope(routine.OrganizationID, routine.WorkspaceID, routine.ChannelID, routine.ID), bson.M{"$set": bson.M{"root_thread_ts": routine.RootThreadTS, "session_id": routine.SessionID, "generation": routine.Generation, "owner_id": routine.OwnerID, "input": routine.Input, "cron": routine.Cron, "timezone": routine.Timezone, "interval": routine.Interval, "next_run": routine.NextRun, "enabled": routine.Enabled, "updated_at": now}, "$setOnInsert": bson.M{"organization_id": routine.OrganizationID, "workspace_id": routine.WorkspaceID, "channel_id": routine.ChannelID, "public_id": routine.ID, "created_at": now}, "$inc": bson.M{"version": 1}}, options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(after)).Decode(&saved)
	return saved, err
}
func (s *MongoStore) UpdateContext(ctx context.Context, routine Routine, expectedVersion int64) (Routine, error) {
	now := s.now().UTC()
	var err error
	routine, err = normalize(routine, now)
	if err != nil {
		return Routine{}, err
	}
	filter := routineScope(routine.OrganizationID, routine.WorkspaceID, routine.ChannelID, routine.ID)
	filter["version"] = expectedVersion
	var saved Routine
	err = s.db.Collection(models.CollectionRoutines).FindOneAndUpdate(ctx, filter, bson.M{"$set": bson.M{"root_thread_ts": routine.RootThreadTS, "session_id": routine.SessionID, "generation": routine.Generation, "owner_id": routine.OwnerID, "input": routine.Input, "cron": routine.Cron, "timezone": routine.Timezone, "interval": routine.Interval, "next_run": routine.NextRun, "enabled": routine.Enabled, "updated_at": now}, "$inc": bson.M{"version": 1}}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&saved)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Routine{}, ErrUpdateConflict
	}
	return saved, err
}
func (s *MongoStore) DeleteContext(ctx context.Context, organizationID, workspaceID, channelID, id string, expectedVersion int64) error {
	filter := routineScope(organizationID, workspaceID, channelID, id)
	filter["version"] = expectedVersion
	result, err := s.db.Collection(models.CollectionRoutines).DeleteOne(ctx, filter)
	if err == nil && result.DeletedCount != 1 {
		return ErrUpdateConflict
	}
	return err
}
func (s *MongoStore) GetContext(ctx context.Context, organizationID, workspaceID, channelID, id string) (Routine, error) {
	var routine Routine
	err := s.db.Collection(models.CollectionRoutines).FindOne(ctx, routineScope(organizationID, workspaceID, channelID, id)).Decode(&routine)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Routine{}, ErrNotFound
	}
	return routine, err
}
func (s *MongoStore) DueContext(ctx context.Context, now time.Time, limit int) ([]Routine, error) {
	find := options.Find().SetSort(bson.D{{Key: "next_run", Value: 1}, {Key: "public_id", Value: 1}})
	if limit > 0 {
		find.SetLimit(int64(limit))
	}
	cursor, err := s.db.Collection(models.CollectionRoutines).Find(ctx, bson.M{"enabled": true, "next_run": bson.M{"$lte": now}}, find)
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	values := make([]Routine, 0)
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
	filter := routineScope(organizationID, workspaceID, channelID, id)
	filter["version"] = current.Version
	result, err := s.db.Collection(models.CollectionRoutines).UpdateOne(ctx, filter, bson.M{"$set": bson.M{"next_run": next, "updated_at": s.now().UTC()}, "$inc": bson.M{"version": 1}})
	if err == nil && result.ModifiedCount != 1 {
		return errors.New("routine advance conflict")
	}
	return err
}
func (s *MongoStore) ListChannel(ctx context.Context, organizationID, workspaceID, channelID string) ([]Routine, error) {
	cursor, err := s.db.Collection(models.CollectionRoutines).Find(ctx, bson.M{"organization_id": organizationID, "workspace_id": workspaceID, "channel_id": channelID}, options.Find().SetSort(bson.D{{Key: "next_run", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	values := make([]Routine, 0)
	err = cursor.All(ctx, &values)
	return values, err
}

func routineScope(organizationID, workspaceID, channelID, id string) bson.M {
	return bson.M{"organization_id": organizationID, "workspace_id": workspaceID, "channel_id": channelID, "public_id": id}
}
func (s *MongoStore) List(ctx context.Context, organizationID string) ([]Routine, error) {
	cursor, err := s.db.Collection(models.CollectionRoutines).Find(ctx, bson.M{"organization_id": organizationID}, options.Find().SetSort(bson.D{{Key: "next_run", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	values := make([]Routine, 0)
	err = cursor.All(ctx, &values)
	return values, err
}

var _ Repository = (*MongoStore)(nil)
