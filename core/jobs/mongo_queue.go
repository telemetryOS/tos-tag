package jobs

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

func NewMongoQueue(db *database.Database) *MongoQueue {
	return &MongoQueue{db: db, now: time.Now}
}

func (q *MongoQueue) Enqueue(ctx context.Context, spec Spec) (Job, bool, error) {
	if err := ValidateSpec(spec); err != nil {
		return Job{}, false, err
	}
	now := q.now().UTC()
	expiresAt := spec.ExpiresAt.UTC()
	if expiresAt.IsZero() {
		expiresAt = now.Add(24 * time.Hour)
	}
	doc := models.Job{
		PublicID:               types.NewID("job"),
		OrganizationID:         spec.OrganizationID,
		WorkspaceID:            spec.WorkspaceID,
		ChannelID:              spec.ChannelID,
		RootThreadTS:           spec.RootThreadTS,
		ReplyInChannel:         spec.ReplyInChannel,
		SessionID:              string(spec.SessionID),
		Generation:             spec.Generation,
		ObservationID:          string(spec.ObservationID),
		RequesterID:            spec.RequesterID,
		IdempotencyKey:         spec.IdempotencyKey,
		Kind:                   spec.Kind,
		Input:                  spec.Input,
		State:                  string(StateQueued),
		MaxAttempts:            spec.MaxAttempts,
		AdmissionReservationID: spec.AdmissionReservationID,
		ResolvedModel:          spec.ResolvedModel, RouteTrace: spec.RouteTrace,
		SteeringEpoch: 1,
		AvailableAt:   now,
		CreatedAt:     now,
		UpdatedAt:     now,
		ExpiresAt:     expiresAt,
		Version:       1,
	}
	_, err := q.db.Collection(models.CollectionJobs).InsertOne(ctx, doc)
	if err == nil {
		return fromModel(doc), true, nil
	}
	if !mongo.IsDuplicateKeyError(err) {
		return Job{}, false, fmt.Errorf("enqueue job: %w", err)
	}
	var existing models.Job
	if err := q.db.Collection(models.CollectionJobs).FindOne(ctx, bson.M{"organization_id": spec.OrganizationID, "idempotency_key": spec.IdempotencyKey}).Decode(&existing); err != nil {
		return Job{}, false, fmt.Errorf("resolve duplicate job: %w", err)
	}
	return fromModel(existing), false, nil
}

func (q *MongoQueue) RecoverExpired(ctx context.Context) error {
	now := q.now().UTC()
	collection := q.db.Collection(models.CollectionJobs)
	if _, err := collection.UpdateMany(ctx, bson.M{
		"state":            string(StateLeased),
		"lease.expires_at": bson.M{"$lte": now},
	}, bson.M{"$set": bson.M{"state": string(StateQueued), "lease": models.Lease{}, "writer_active": false, "updated_at": now}, "$inc": bson.M{"version": 1}}); err != nil {
		return fmt.Errorf("recover leased jobs: %w", err)
	}
	if _, err := collection.UpdateMany(ctx, bson.M{
		"state":            bson.M{"$in": []string{string(StatePreparing), string(StateRunning)}},
		"lease.expires_at": bson.M{"$lte": now},
	}, bson.M{"$set": bson.M{"state": string(StateNeedsReconciliation), "lease": models.Lease{}, "writer_active": false, "updated_at": now}, "$inc": bson.M{"version": 1}}); err != nil {
		return fmt.Errorf("fence expired running jobs: %w", err)
	}
	return nil
}

func (q *MongoQueue) Claim(ctx context.Context, worker types.WorkerID, leaseDuration time.Duration) (Job, error) {
	now := q.now().UTC()
	filter := bson.M{
		"state":        string(StateQueued),
		"available_at": bson.M{"$lte": now},
		"expires_at":   bson.M{"$gt": now},
		"$expr":        bson.M{"$lt": []any{"$attempt", "$max_attempts"}},
	}
	cursor, err := q.db.Collection(models.CollectionJobs).Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "available_at", Value: 1}, {Key: "created_at", Value: 1}}).SetLimit(64))
	if err != nil {
		return Job{}, fmt.Errorf("find runnable jobs: %w", err)
	}
	defer cursor.Close(ctx)
	var candidates []models.Job
	if err := cursor.All(ctx, &candidates); err != nil {
		return Job{}, fmt.Errorf("decode runnable jobs: %w", err)
	}
	for _, candidate := range candidates {
		token := types.NewID("lease")
		update := bson.M{"$set": bson.M{"state": string(StateLeased), "lease.owner": string(worker), "lease.token": token, "lease.expires_at": now.Add(leaseDuration), "lease.heartbeat_at": now, "updated_at": now, "writer_active": true}, "$inc": bson.M{"attempt": 1, "version": 1}}
		var claimed models.Job
		err := q.db.Collection(models.CollectionJobs).FindOneAndUpdate(ctx, bson.M{"public_id": candidate.PublicID, "state": string(StateQueued), "version": candidate.Version}, update, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&claimed)
		if err == nil {
			return fromModel(claimed), nil
		}
		if errors.Is(err, mongo.ErrNoDocuments) || mongo.IsDuplicateKeyError(err) {
			continue
		}
		return Job{}, fmt.Errorf("claim job: %w", err)
	}
	return Job{}, ErrNoRunnableJob
}

func (q *MongoQueue) Transition(ctx context.Context, id types.JobID, leaseToken string, to State, mutate func(*Job)) (Job, error) {
	current, err := q.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if current.Lease.Token != leaseToken || !current.Lease.ExpiresAt.After(q.now().UTC()) {
		return Job{}, ErrLeaseLost
	}
	if !CanTransition(current.State, to) {
		return Job{}, ErrInvalidState
	}
	if mutate != nil {
		mutate(&current)
	}
	now := q.now().UTC()
	set := transitionSet(current, to, now)
	if to == StateSucceeded || to == StateFailed || to == StateCancelled || to == StateRetryWait || to == StateNeedsReconciliation || to == StateWaitingApproval {
		set["lease"] = models.Lease{}
		set["writer_active"] = to == StateWaitingApproval
	}
	filter := bson.M{
		"public_id":        string(id),
		"state":            string(current.State),
		"version":          current.Version,
		"lease.token":      leaseToken,
		"lease.expires_at": bson.M{"$gt": now},
	}
	after := options.After
	var updated models.Job
	if err := q.db.Collection(models.CollectionJobs).FindOneAndUpdate(ctx, filter, bson.M{"$set": set, "$inc": bson.M{"version": 1}}, options.FindOneAndUpdate().SetReturnDocument(after)).Decode(&updated); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return Job{}, ErrLeaseLost
		}
		return Job{}, fmt.Errorf("transition job: %w", err)
	}
	return fromModel(updated), nil
}

func (q *MongoQueue) SetProgressMessageTS(ctx context.Context, id types.JobID, leaseToken, messageTS string) (Job, error) {
	if messageTS == "" {
		return Job{}, ErrInvalidState
	}
	now := q.now().UTC()
	var updated models.Job
	err := q.db.Collection(models.CollectionJobs).FindOneAndUpdate(ctx, bson.M{
		"public_id":        string(id),
		"state":            string(StateRunning),
		"lease.token":      leaseToken,
		"lease.expires_at": bson.M{"$gt": now},
	}, bson.M{
		"$set": bson.M{"progress_message_ts": messageTS, "updated_at": now},
		"$inc": bson.M{"version": 1},
	}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&updated)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Job{}, ErrLeaseLost
	}
	if err != nil {
		return Job{}, fmt.Errorf("persist job progress message: %w", err)
	}
	return fromModel(updated), nil
}

// transitionSet is the Mongo persistence side of Queue.Transition's mutate
// contract. Keep it aligned with the fields callers are permitted to change
// through the transition callback and with MemoryQueue.Transition.
func transitionSet(current Job, to State, now time.Time) bson.M {
	return bson.M{
		"state":                string(to),
		"result":               current.Result,
		"failure_reason":       current.FailureReason,
		"available_at":         current.AvailableAt,
		"approval_id":          current.ApprovalID,
		"approved_action_hash": current.ApprovedActionHash,
		"progress_message_ts":  current.ProgressMessageTS,
		"resolved_model":       current.ResolvedModel,
		"route_trace":          current.RouteTrace,
		"updated_at":           now,
	}
}

func (q *MongoQueue) SuspendForApproval(ctx context.Context, id types.JobID, leaseToken, approvalID string) (Job, error) {
	if approvalID == "" {
		return Job{}, ErrInvalidState
	}
	return q.Transition(ctx, id, leaseToken, StateWaitingApproval, func(job *Job) {
		job.ApprovalID = approvalID
		job.ApprovedActionHash = ""
	})
}

func (q *MongoQueue) ResumeFromApproval(ctx context.Context, id types.JobID, approvalID, actionHash string) (Job, error) {
	if approvalID == "" || actionHash == "" {
		return Job{}, ErrInvalidState
	}
	now := q.now().UTC()
	var updated models.Job
	err := q.db.Collection(models.CollectionJobs).FindOneAndUpdate(ctx,
		bson.M{"public_id": string(id), "state": string(StateWaitingApproval), "approval_id": approvalID},
		bson.M{"$set": bson.M{"state": string(StateQueued), "approved_action_hash": actionHash, "available_at": now, "lease": models.Lease{}, "writer_active": false, "updated_at": now}, "$inc": bson.M{"attempt": -1, "steering_epoch": 1, "version": 1}},
		options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&updated)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Job{}, ErrInvalidState
	}
	if err != nil {
		return Job{}, err
	}
	return fromModel(updated), nil
}

func (q *MongoQueue) Cancel(ctx context.Context, id types.JobID, reason string) (Job, error) {
	now := q.now().UTC()
	after := options.After
	var updated models.Job
	immediate := bson.M{"$in": bson.A{"$state", bson.A{string(StateQueued), string(StateRetryWait), string(StateWaitingApproval)}}}
	pipeline := mongo.Pipeline{{{Key: "$set", Value: bson.M{
		"state":          bson.M{"$switch": bson.M{"branches": bson.A{bson.M{"case": immediate, "then": string(StateCancelled)}, bson.M{"case": bson.M{"$in": bson.A{"$state", bson.A{string(StateLeased), string(StatePreparing), string(StateRunning)}}}, "then": string(StateCancelling)}}, "default": "$state"}},
		"failure_reason": reason, "steering_epoch": bson.M{"$add": bson.A{"$steering_epoch", 1}},
		"lease": bson.M{"$cond": bson.A{immediate, models.Lease{}, "$lease"}}, "writer_active": bson.M{"$cond": bson.A{immediate, false, "$writer_active"}},
		"updated_at": now, "version": bson.M{"$add": bson.A{"$version", 1}},
	}}}}
	err := q.db.Collection(models.CollectionJobs).FindOneAndUpdate(ctx, bson.M{"public_id": string(id), "state": bson.M{"$in": []string{string(StateQueued), string(StateRetryWait), string(StateWaitingApproval), string(StateLeased), string(StatePreparing), string(StateRunning)}}}, pipeline, options.FindOneAndUpdate().SetReturnDocument(after)).Decode(&updated)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Job{}, ErrInvalidState
	}
	if err != nil {
		return Job{}, err
	}
	return fromModel(updated), nil
}
func (q *MongoQueue) MarkCompletedUndelivered(ctx context.Context, id types.JobID, reason string) (Job, error) {
	var updated models.Job
	err := q.db.Collection(models.CollectionJobs).FindOneAndUpdate(ctx, bson.M{"public_id": string(id), "state": bson.M{"$in": []string{string(StateSucceeded), string(StateCompletedUndelivered)}}}, bson.M{"$set": bson.M{"state": string(StateCompletedUndelivered), "failure_reason": reason, "updated_at": q.now().UTC()}, "$inc": bson.M{"version": 1}}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&updated)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Job{}, ErrInvalidState
	}
	if err != nil {
		return Job{}, err
	}
	return fromModel(updated), nil
}

func (q *MongoQueue) Heartbeat(ctx context.Context, id types.JobID, leaseToken string, duration time.Duration) error {
	now := q.now().UTC()
	result, err := q.db.Collection(models.CollectionJobs).UpdateOne(ctx, bson.M{
		"public_id":        string(id),
		"lease.token":      leaseToken,
		"lease.expires_at": bson.M{"$gt": now},
	}, bson.M{"$set": bson.M{"lease.heartbeat_at": now, "lease.expires_at": now.Add(duration), "updated_at": now}, "$inc": bson.M{"version": 1}})
	if err != nil {
		return fmt.Errorf("heartbeat job: %w", err)
	}
	if result.MatchedCount == 0 {
		return ErrLeaseLost
	}
	return nil
}

func (q *MongoQueue) Requeue(ctx context.Context, id types.JobID, leaseToken, reason string, delay time.Duration) (Job, error) {
	return q.Transition(ctx, id, leaseToken, StateRetryWait, func(job *Job) {
		job.FailureReason = reason
		job.AvailableAt = q.now().UTC().Add(delay)
	})
}

func (q *MongoQueue) ReleaseRetryWait(ctx context.Context, id types.JobID) (Job, error) {
	current, err := q.Get(ctx, id)
	if err != nil {
		return Job{}, err
	}
	if current.State != StateRetryWait || current.AvailableAt.After(q.now().UTC()) {
		return Job{}, ErrInvalidState
	}
	next, reason := StateQueued, current.FailureReason
	if current.Attempt >= current.MaxAttempts {
		next, reason = StateFailed, "attempts_exhausted"
	}
	after := options.After
	var updated models.Job
	if err := q.db.Collection(models.CollectionJobs).FindOneAndUpdate(ctx, bson.M{"public_id": string(id), "state": string(StateRetryWait), "version": current.Version}, bson.M{"$set": bson.M{"state": string(next), "failure_reason": reason, "updated_at": q.now().UTC()}, "$inc": bson.M{"version": 1}}, options.FindOneAndUpdate().SetReturnDocument(after)).Decode(&updated); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return Job{}, ErrInvalidState
		}
		return Job{}, fmt.Errorf("release retry wait: %w", err)
	}
	return fromModel(updated), nil
}

func (q *MongoQueue) Get(ctx context.Context, id types.JobID) (Job, error) {
	var doc models.Job
	if err := q.db.Collection(models.CollectionJobs).FindOne(ctx, bson.M{"public_id": string(id)}).Decode(&doc); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return Job{}, ErrJobNotFound
		}
		return Job{}, fmt.Errorf("get job: %w", err)
	}
	return fromModel(doc), nil
}

func (q *MongoQueue) Count(ctx context.Context) (int, error) {
	count, err := q.db.Collection(models.CollectionJobs).CountDocuments(ctx, bson.M{})
	if err != nil {
		return 0, fmt.Errorf("count jobs: %w", err)
	}
	return int(count), nil
}

func (q *MongoQueue) List(ctx context.Context) ([]Job, error) {
	return q.list(ctx, bson.M{})
}

func (q *MongoQueue) ListReconciliation(ctx context.Context, now time.Time) ([]Job, error) {
	return q.list(ctx, bson.M{"$or": bson.A{
		bson.M{"state": string(StateWaitingApproval)},
		bson.M{"state": string(StateNeedsReconciliation)},
		bson.M{"state": string(StateQueued), "$or": bson.A{
			bson.M{"expires_at": bson.M{"$lte": now}},
			bson.M{"$expr": bson.M{"$gte": bson.A{"$attempt", "$max_attempts"}}},
		}},
		bson.M{"state": string(StateRetryWait), "available_at": bson.M{"$lte": now}},
		bson.M{"state": string(StateSucceeded), "final_delivery_enqueued": bson.M{"$ne": true}, "expires_at": bson.M{"$gt": now}},
	}})
}

func (q *MongoQueue) MarkFinalDeliveryEnqueued(ctx context.Context, id types.JobID) error {
	result, err := q.db.Collection(models.CollectionJobs).UpdateOne(ctx,
		bson.M{"public_id": string(id), "state": string(StateSucceeded)},
		bson.M{"$set": bson.M{"final_delivery_enqueued": true, "updated_at": q.now().UTC()}, "$inc": bson.M{"version": 1}},
	)
	if err != nil {
		return fmt.Errorf("mark final delivery enqueued: %w", err)
	}
	if result.MatchedCount == 0 {
		return ErrInvalidState
	}
	return nil
}

func (q *MongoQueue) ListOrganization(ctx context.Context, organizationID string) ([]Job, error) {
	if organizationID == "" {
		return nil, errors.New("organization_id is required")
	}
	cursor, err := q.db.Collection(models.CollectionJobs).Find(ctx, bson.M{"organization_id": organizationID}, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(organizationListLimit))
	if err != nil {
		return nil, fmt.Errorf("list recent jobs: %w", err)
	}
	defer cursor.Close(ctx)
	var docs []models.Job
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode recent jobs: %w", err)
	}
	result := make([]Job, len(docs))
	for i, doc := range docs {
		result[i] = fromModel(doc)
	}
	return result, nil
}

func (q *MongoQueue) list(ctx context.Context, filter bson.M) ([]Job, error) {
	cursor, err := q.db.Collection(models.CollectionJobs).Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "created_at", Value: 1}}))
	if err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	defer cursor.Close(ctx)
	var docs []models.Job
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, fmt.Errorf("decode jobs: %w", err)
	}
	result := make([]Job, len(docs))
	for i, doc := range docs {
		result[i] = fromModel(doc)
	}
	return result, nil
}

func fromModel(doc models.Job) Job {
	var result types.SlackResult
	var resolved types.ResolvedModel
	var trace types.DecisionTrace
	if doc.Result != nil {
		if encoded, err := bson.Marshal(doc.Result); err == nil {
			_ = bson.Unmarshal(encoded, &result)
		}
	}
	if doc.ResolvedModel != nil {
		if encoded, err := bson.Marshal(doc.ResolvedModel); err == nil {
			_ = bson.Unmarshal(encoded, &resolved)
		}
	}
	if doc.RouteTrace != nil {
		if encoded, err := bson.Marshal(doc.RouteTrace); err == nil {
			_ = bson.Unmarshal(encoded, &trace)
		}
	}
	return Job{
		ID: types.JobID(doc.PublicID), OrganizationID: doc.OrganizationID, WorkspaceID: doc.WorkspaceID,
		ChannelID: doc.ChannelID, RootThreadTS: doc.RootThreadTS, ReplyInChannel: doc.ReplyInChannel, SessionID: types.SessionID(doc.SessionID),
		Generation: doc.Generation, ObservationID: types.ObservationID(doc.ObservationID), RequesterID: doc.RequesterID, IdempotencyKey: doc.IdempotencyKey,
		Kind: doc.Kind, Input: doc.Input, State: State(doc.State), Attempt: doc.Attempt, MaxAttempts: doc.MaxAttempts,
		AdmissionReservationID: doc.AdmissionReservationID,
		ResolvedModel:          resolved, RouteTrace: trace,
		SteeringEpoch: doc.SteeringEpoch, Lease: Lease{Owner: types.WorkerID(doc.Lease.Owner), Token: doc.Lease.Token, ExpiresAt: doc.Lease.ExpiresAt, Heartbeat: doc.Lease.Heartbeat},
		Result: result, FailureReason: doc.FailureReason, ApprovalID: doc.ApprovalID, ApprovedActionHash: doc.ApprovedActionHash, ProgressMessageTS: doc.ProgressMessageTS, FinalDeliveryEnqueued: doc.FinalDeliveryEnqueued,
		AvailableAt: doc.AvailableAt, CreatedAt: doc.CreatedAt, UpdatedAt: doc.UpdatedAt, ExpiresAt: doc.ExpiresAt, Version: doc.Version,
	}
}
