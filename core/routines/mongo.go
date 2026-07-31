package routines

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/telemetryos/tos-tag/core/database"
	"github.com/telemetryos/tos-tag/models"
)

type MongoStore struct {
	db  *database.Database
	now func() time.Time
}

func NewMongoStore(db *database.Database) *MongoStore { return &MongoStore{db: db, now: time.Now} }
func (s *MongoStore) PutContext(ctx context.Context, routine Routine) (Routine, error) {
	if _, err := validate(routine); err != nil {
		return Routine{}, err
	}
	now := s.now().UTC()
	after := options.After
	var saved Routine
	err := s.db.Collection(models.CollectionRoutines).FindOneAndUpdate(ctx, bson.M{"organization_id": routine.OrganizationID, "public_id": routine.ID}, bson.M{"$set": bson.M{"workspace_id": routine.WorkspaceID, "channel_id": routine.ChannelID, "root_thread_ts": routine.RootThreadTS, "session_id": routine.SessionID, "generation": routine.Generation, "owner_id": routine.OwnerID, "input": routine.Input, "interval": routine.Interval, "next_run": routine.NextRun, "enabled": routine.Enabled, "updated_at": now}, "$setOnInsert": bson.M{"organization_id": routine.OrganizationID, "public_id": routine.ID, "created_at": now}, "$inc": bson.M{"version": 1}}, options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(after)).Decode(&saved)
	return saved, err
}
func (s *MongoStore) DueContext(ctx context.Context, now time.Time, limit int) ([]Routine, error) {
	cursor, err := s.db.Collection(models.CollectionRoutines).Find(ctx, bson.M{"enabled": true, "next_run": bson.M{"$lte": now}}, options.Find().SetSort(bson.D{{Key: "next_run", Value: 1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var values []Routine
	err = cursor.All(ctx, &values)
	return values, err
}
func (s *MongoStore) AdvanceContext(ctx context.Context, organizationID, id string, from time.Time) error {
	var current Routine
	err := s.db.Collection(models.CollectionRoutines).FindOne(ctx, bson.M{"organization_id": organizationID, "public_id": id}).Decode(&current)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return errors.New("routine not found")
	}
	if err != nil {
		return err
	}
	next := current.NextRun
	for !next.After(from) {
		next = next.Add(current.Interval)
	}
	result, err := s.db.Collection(models.CollectionRoutines).UpdateOne(ctx, bson.M{"organization_id": organizationID, "public_id": id, "version": current.Version}, bson.M{"$set": bson.M{"next_run": next, "updated_at": s.now().UTC()}, "$inc": bson.M{"version": 1}})
	if err == nil && result.ModifiedCount != 1 {
		return errors.New("routine advance conflict")
	}
	return err
}
func (s *MongoStore) List(ctx context.Context, organizationID string) ([]Routine, error) {
	cursor, err := s.db.Collection(models.CollectionRoutines).Find(ctx, bson.M{"organization_id": organizationID}, options.Find().SetSort(bson.D{{Key: "next_run", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var values []Routine
	err = cursor.All(ctx, &values)
	return values, err
}
func validate(r Routine) (Routine, error) {
	if r.ID == "" || r.OrganizationID == "" || r.WorkspaceID == "" || r.ChannelID == "" || r.SessionID == "" || r.Generation <= 0 || r.OwnerID == "" || r.Interval < time.Minute || r.NextRun.IsZero() {
		return Routine{}, errors.New("invalid routine")
	}
	return r, nil
}

var _ Repository = (*MongoStore)(nil)
