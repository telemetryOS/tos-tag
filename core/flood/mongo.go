package flood

import (
	"context"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/telemetryos/tos-tag/core/database"
	"github.com/telemetryos/tos-tag/models"
)

type Mongo struct {
	db     *database.Database
	now    func() time.Time
	limit  int64
	window time.Duration
}

type mongoBucket struct {
	ID             string    `bson:"_id"`
	Count          int64     `bson:"count"`
	WindowStart    time.Time `bson:"window_start"`
	WindowEnd      time.Time `bson:"window_end"`
	OrganizationID string    `bson:"organization_id"`
	TeamID         string    `bson:"team_id"`
	ExpiresAt      time.Time `bson:"expires_at"`
}

func NewMongo(db *database.Database, limit int, window time.Duration) (*Mongo, error) {
	if db == nil {
		return nil, fmt.Errorf("database is required")
	}
	if limit <= 0 || window <= 0 {
		return nil, fmt.Errorf("flood-protection limit and window must be positive")
	}
	return &Mongo{db: db, now: time.Now, limit: int64(limit), window: window}, nil
}

func (m *Mongo) Admit(ctx context.Context, scope Scope) (Result, error) {
	if err := validateScope(scope); err != nil {
		return Result{}, err
	}
	now := m.now().UTC()
	start := windowStart(now, m.window)
	end := start.Add(m.window)
	var bucket mongoBucket
	filter := bson.M{"_id": bucketID(scope, start, m.window)}
	update := bson.M{
		"$inc": bson.M{"count": 1},
		"$set": bson.M{"updated_at": now},
		"$setOnInsert": bson.M{
			"organization_id": scope.OrganizationID,
			"team_id":         scope.TeamID,
			"window_start":    start,
			"window_end":      end,
			"expires_at":      end.Add(24 * time.Hour),
			"created_at":      now,
		},
	}
	err := m.db.Collection(models.CollectionClassifierFloodBuckets).FindOneAndUpdate(ctx, filter, update, options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)).Decode(&bucket)
	if mongo.IsDuplicateKeyError(err) {
		// Two processes can race while creating the first bucket. Retry as an
		// update so both calls are charged rather than failing open or dropping a
		// legitimate message because of a transient upsert collision.
		err = m.db.Collection(models.CollectionClassifierFloodBuckets).FindOneAndUpdate(ctx, filter, update, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&bucket)
	}
	if err != nil {
		return Result{}, fmt.Errorf("increment classifier flood-protection bucket: %w", err)
	}
	return Result{Allowed: bucket.Count <= m.limit, Count: bucket.Count, Limit: m.limit, WindowStart: start, WindowEnd: end}, nil
}

var _ Gate = (*Mongo)(nil)
