package deliveries

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/telemetryos/tos-tag/core/database"
	"github.com/telemetryos/tos-tag/models"
	"github.com/telemetryos/tos-tag/types"
)

type MongoQueue struct {
	db  *database.Database
	now func() time.Time
}

func NewMongoQueue(db *database.Database) *MongoQueue { return &MongoQueue{db: db, now: time.Now} }

func (q *MongoQueue) Enqueue(ctx context.Context, spec Spec) (Record, bool, error) {
	if spec.OrganizationID == "" || (spec.JobID == "") == (spec.DecisionID == "") || spec.IdempotencyKey == "" || spec.Destination.TeamID == "" || spec.Destination.ChannelID == "" || spec.MaxAttempts <= 0 {
		return Record{}, false, errors.New("invalid delivery specification")
	}
	now := q.now().UTC()
	expiresAt := spec.ExpiresAt.UTC()
	if expiresAt.IsZero() {
		expiresAt = now.Add(24 * time.Hour)
	}
	doc := models.Delivery{PublicID: types.NewID("dlv"), OrganizationID: spec.OrganizationID, JobID: string(spec.JobID), DecisionID: spec.DecisionID, IdempotencyKey: spec.IdempotencyKey, TeamID: spec.Destination.TeamID, ChannelID: spec.Destination.ChannelID, ThreadTS: spec.Destination.ThreadTS, UpdateTS: spec.Destination.UpdateTS, StreamTS: spec.Destination.StreamTS, Result: spec.Result, Status: string(StatusPending), MaxAttempts: spec.MaxAttempts, RetryAt: now, CreatedAt: now, UpdatedAt: now, ExpiresAt: expiresAt, Version: 1}
	_, err := q.db.Collection(models.CollectionDeliveries).InsertOne(ctx, doc)
	if err == nil {
		return deliveryFromModel(doc), true, nil
	}
	if !mongo.IsDuplicateKeyError(err) {
		return Record{}, false, fmt.Errorf("enqueue delivery: %w", err)
	}
	var existing models.Delivery
	if err := q.db.Collection(models.CollectionDeliveries).FindOne(ctx, bson.M{"organization_id": spec.OrganizationID, "idempotency_key": spec.IdempotencyKey}).Decode(&existing); err != nil {
		return Record{}, false, fmt.Errorf("resolve duplicate delivery: %w", err)
	}
	return deliveryFromModel(existing), false, nil
}

func (q *MongoQueue) Claim(ctx context.Context, worker types.WorkerID, duration time.Duration) (Record, error) {
	now := q.now().UTC()
	if _, err := q.db.Collection(models.CollectionDeliveries).UpdateMany(ctx, bson.M{"status": string(StatusLeased), "lease.expires_at": bson.M{"$lte": now}}, bson.M{"$set": bson.M{"status": string(StatusPending), "lease": models.Lease{}, "updated_at": now}, "$inc": bson.M{"version": 1}}); err != nil {
		return Record{}, fmt.Errorf("recover expired delivery leases: %w", err)
	}
	if _, err := q.db.Collection(models.CollectionDeliveries).UpdateMany(ctx, bson.M{
		"status": bson.M{"$in": []string{string(StatusPending), string(StatusRetryWait)}},
		"$expr":  bson.M{"$gte": []any{"$attempt", "$max_attempts"}},
	}, bson.M{"$set": bson.M{"status": string(StatusAbandoned), "failure_reason": "attempts_exhausted", "lease": models.Lease{}, "updated_at": now}, "$inc": bson.M{"version": 1}}); err != nil {
		return Record{}, fmt.Errorf("abandon exhausted deliveries: %w", err)
	}
	after := options.After
	var doc models.Delivery
	err := q.db.Collection(models.CollectionDeliveries).FindOneAndUpdate(ctx, bson.M{
		"status": bson.M{"$in": []string{string(StatusPending), string(StatusRetryWait)}}, "retry_at": bson.M{"$lte": now}, "expires_at": bson.M{"$gt": now}, "$expr": bson.M{"$lt": []any{"$attempt", "$max_attempts"}},
	}, bson.M{"$set": bson.M{"status": string(StatusLeased), "lease.owner": string(worker), "lease.token": types.NewID("lease"), "lease.expires_at": now.Add(duration), "updated_at": now}, "$inc": bson.M{"attempt": 1, "version": 1}}, options.FindOneAndUpdate().SetSort(bson.D{{Key: "created_at", Value: 1}}).SetReturnDocument(after)).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Record{}, ErrNoPendingDelivery
	}
	if err != nil {
		return Record{}, fmt.Errorf("claim delivery: %w", err)
	}
	return deliveryFromModel(doc), nil
}

func (q *MongoQueue) Complete(ctx context.Context, id types.DeliveryID, token string, result types.SlackDeliveryResult) (Record, error) {
	return q.updateLeased(ctx, id, token, bson.M{"status": string(StatusDelivered), "slack_message_ts": result.MessageTS, "lease": models.Lease{}, "updated_at": q.now().UTC()})
}

func (q *MongoQueue) Retry(ctx context.Context, id types.DeliveryID, token, reason string, delay time.Duration) (Record, error) {
	current, err := q.Get(ctx, id)
	if err != nil {
		return Record{}, err
	}
	status := StatusRetryWait
	if current.Attempt >= current.MaxAttempts {
		status = StatusAbandoned
	}
	return q.updateLeased(ctx, id, token, bson.M{"status": string(status), "failure_reason": reason, "retry_at": q.now().UTC().Add(delay), "lease": models.Lease{}, "updated_at": q.now().UTC()})
}

func (q *MongoQueue) Abandon(ctx context.Context, id types.DeliveryID, token, reason string) (Record, error) {
	return q.updateLeased(ctx, id, token, bson.M{"status": string(StatusAbandoned), "failure_reason": reason, "lease": models.Lease{}, "updated_at": q.now().UTC()})
}

func (q *MongoQueue) updateLeased(ctx context.Context, id types.DeliveryID, token string, set bson.M) (Record, error) {
	after := options.After
	var doc models.Delivery
	err := q.db.Collection(models.CollectionDeliveries).FindOneAndUpdate(ctx, bson.M{"public_id": string(id), "status": string(StatusLeased), "lease.token": token, "lease.expires_at": bson.M{"$gt": q.now().UTC()}}, bson.M{"$set": set, "$inc": bson.M{"version": 1}}, options.FindOneAndUpdate().SetReturnDocument(after)).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Record{}, ErrDeliveryLeaseLost
	}
	if err != nil {
		return Record{}, fmt.Errorf("update delivery: %w", err)
	}
	return deliveryFromModel(doc), nil
}

func (q *MongoQueue) Get(ctx context.Context, id types.DeliveryID) (Record, error) {
	var doc models.Delivery
	if err := q.db.Collection(models.CollectionDeliveries).FindOne(ctx, bson.M{"public_id": string(id)}).Decode(&doc); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return Record{}, ErrDeliveryNotFound
		}
		return Record{}, fmt.Errorf("get delivery: %w", err)
	}
	return deliveryFromModel(doc), nil
}

func (q *MongoQueue) FindByIdempotency(ctx context.Context, organizationID, idempotencyKey string) (Record, error) {
	if organizationID == "" || idempotencyKey == "" {
		return Record{}, ErrDeliveryNotFound
	}
	var doc models.Delivery
	if err := q.db.Collection(models.CollectionDeliveries).FindOne(ctx, bson.M{"organization_id": organizationID, "idempotency_key": idempotencyKey}).Decode(&doc); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return Record{}, ErrDeliveryNotFound
		}
		return Record{}, fmt.Errorf("find delivery by idempotency: %w", err)
	}
	return deliveryFromModel(doc), nil
}

func (q *MongoQueue) Count(ctx context.Context) (int, error) {
	count, err := q.db.Collection(models.CollectionDeliveries).CountDocuments(ctx, bson.M{})
	if err != nil {
		return 0, fmt.Errorf("count deliveries: %w", err)
	}
	return int(count), nil
}

func (q *MongoQueue) List(ctx context.Context) ([]Record, error) {
	return q.list(ctx, bson.M{})
}

func (q *MongoQueue) ListOrganization(ctx context.Context, organizationID string) ([]Record, error) {
	if organizationID == "" {
		return nil, errors.New("organization_id is required")
	}
	cursor, err := q.db.Collection(models.CollectionDeliveries).Find(ctx, bson.M{"organization_id": organizationID}, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(organizationListLimit))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var docs []models.Delivery
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, err
	}
	result := make([]Record, len(docs))
	for i, doc := range docs {
		result[i] = deliveryFromModel(doc)
	}
	return result, nil
}

func (q *MongoQueue) list(ctx context.Context, filter bson.M) ([]Record, error) {
	cursor, err := q.db.Collection(models.CollectionDeliveries).Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var docs []models.Delivery
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, err
	}
	result := make([]Record, len(docs))
	for i, doc := range docs {
		result[i] = deliveryFromModel(doc)
	}
	return result, nil
}

func deliveryFromModel(doc models.Delivery) Record {
	var result types.SlackResult
	if doc.Result != nil {
		if encoded, err := bson.Marshal(doc.Result); err == nil {
			_ = bson.Unmarshal(encoded, &result)
		}
	}
	return Record{ID: types.DeliveryID(doc.PublicID), OrganizationID: doc.OrganizationID, JobID: types.JobID(doc.JobID), DecisionID: doc.DecisionID, IdempotencyKey: doc.IdempotencyKey, Destination: types.SlackDestination{TeamID: doc.TeamID, ChannelID: doc.ChannelID, ThreadTS: doc.ThreadTS, UpdateTS: doc.UpdateTS, StreamTS: doc.StreamTS}, Result: result, Status: Status(doc.Status), Attempt: doc.Attempt, MaxAttempts: doc.MaxAttempts, RetryAt: doc.RetryAt, Lease: Lease{Owner: types.WorkerID(doc.Lease.Owner), Token: doc.Lease.Token, ExpiresAt: doc.Lease.ExpiresAt}, SlackMessageTS: doc.SlackMessageTS, FailureReason: doc.FailureReason, CreatedAt: doc.CreatedAt, UpdatedAt: doc.UpdatedAt, ExpiresAt: doc.ExpiresAt, Version: doc.Version}
}
