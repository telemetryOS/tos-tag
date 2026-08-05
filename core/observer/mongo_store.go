package observer

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/telemetryos/tos-tag/core/database"
	"github.com/telemetryos/tos-tag/models"
	"github.com/telemetryos/tos-tag/types"
)

type MongoStore struct {
	db                   *database.Database
	observationRetention time.Duration
	now                  func() time.Time
}

func NewMongoStore(db *database.Database, observationRetention time.Duration) *MongoStore {
	return &MongoStore{db: db, observationRetention: observationRetention, now: time.Now}
}

func (s *MongoStore) Accept(ctx context.Context, envelope types.SlackEnvelope) (Acceptance, error) {
	return s.accept(ctx, envelope, "pending", "pending")
}

// Import persists user-authorized Slack history as resolved context. It shares
// the live event idempotency key so a startup history row and a later Slack
// retry converge on one observation and one current-message projection.
func (s *MongoStore) Import(ctx context.Context, envelope types.SlackEnvelope) (Acceptance, error) {
	return s.accept(ctx, envelope, "authorized", "resolved")
}

func (s *MongoStore) accept(ctx context.Context, envelope types.SlackEnvelope, scopeState, decisionState string) (Acceptance, error) {
	if err := ValidateEnvelope(envelope); err != nil {
		return Acceptance{}, err
	}
	filter := bson.M{"team_id": envelope.TeamID, "event_id": envelope.EventID}
	var existing models.Observation
	err := s.db.Collection(models.CollectionObservations).FindOne(ctx, filter).Decode(&existing)
	if err == nil {
		if err := s.applyProjection(ctx, envelope, existing); err != nil {
			return Acceptance{}, err
		}
		return Acceptance{Observation: existing, Duplicate: true}, nil
	}
	if err != mongo.ErrNoDocuments {
		return Acceptance{}, fmt.Errorf("look up observation: %w", err)
	}

	now := s.now().UTC()
	channelSeq, err := s.nextSequence(ctx, models.CollectionChannelCounters, envelope.OrganizationID+"/"+envelope.TeamID+"/"+envelope.ChannelID, now)
	if err != nil {
		return Acceptance{}, fmt.Errorf("allocate channel sequence: %w", err)
	}
	organizationSeq, err := s.nextSequence(ctx, models.CollectionOrganizationCounts, envelope.OrganizationID, now)
	if err != nil {
		return Acceptance{}, fmt.Errorf("allocate organization sequence: %w", err)
	}
	at := eventTime(envelope, now)
	observation := models.Observation{
		PublicID:                types.NewID("obs"),
		OrganizationID:          envelope.OrganizationID,
		TeamID:                  envelope.TeamID,
		ChannelID:               envelope.ChannelID,
		EventID:                 envelope.EventID,
		EnvelopeID:              envelope.EnvelopeID,
		ReceivedSeq:             channelSeq,
		OrganizationReceivedSeq: organizationSeq,
		SlackEventTime:          at,
		ReceivedAt:              now,
		MessageTS:               envelope.MessageTS,
		RootThreadTS:            envelope.RootThreadTS(),
		UserID:                  envelope.UserID,
		BotID:                   envelope.BotID,
		EventType:               string(envelope.Kind),
		Subtype:                 envelope.Subtype,
		Text:                    envelope.Text,
		Images:                  append([]types.SlackImageRef(nil), envelope.Images...),
		MutationTargetTS:        envelope.TargetTS,
		ScopeState:              scopeState,
		DecisionState:           decisionState,
		Restricted:              envelope.Restricted,
		IsMention:               envelope.IsMention,
		OriginTag:               envelope.OriginTag,
		CreatedAt:               now,
		ExpiresAt:               at.Add(s.observationRetention),
		Version:                 1,
	}
	if _, err := s.db.Collection(models.CollectionObservations).InsertOne(ctx, observation); err != nil {
		if !mongo.IsDuplicateKeyError(err) {
			return Acceptance{}, fmt.Errorf("insert observation: %w", err)
		}
		if err := s.db.Collection(models.CollectionObservations).FindOne(ctx, filter).Decode(&existing); err != nil {
			return Acceptance{}, fmt.Errorf("resolve duplicate observation: %w", err)
		}
		if err := s.applyProjection(ctx, envelope, existing); err != nil {
			return Acceptance{}, err
		}
		return Acceptance{Observation: existing, Duplicate: true}, nil
	}
	if err := s.applyProjection(ctx, envelope, observation); err != nil {
		return Acceptance{}, err
	}
	return Acceptance{Observation: observation}, nil
}

func (s *MongoStore) ClaimPending(ctx context.Context, owner string, lease time.Duration) (models.Observation, error) {
	if owner == "" || lease <= 0 {
		return models.Observation{}, ErrInvalidEnvelope
	}
	now := s.now().UTC()
	filter := bson.M{
		"expires_at": bson.M{"$gt": now},
		"$or": bson.A{
			bson.M{"decision_state": "pending"},
			bson.M{"decision_state": "processing", "decision_lease_expires_at": bson.M{"$lte": now}},
		},
	}
	update := bson.M{"$set": bson.M{"decision_state": "processing", "decision_lease_owner": owner, "decision_lease_token": types.NewID("obslease"), "decision_lease_expires_at": now.Add(lease)}, "$inc": bson.M{"version": 1}}
	var observation models.Observation
	err := s.db.Collection(models.CollectionObservations).FindOneAndUpdate(ctx, filter, update, options.FindOneAndUpdate().SetSort(bson.D{{Key: "received_at", Value: 1}, {Key: "organization_received_seq", Value: 1}}).SetReturnDocument(options.After)).Decode(&observation)
	if err == mongo.ErrNoDocuments {
		return models.Observation{}, ErrNoPendingObservation
	}
	if err != nil {
		return models.Observation{}, fmt.Errorf("claim observation: %w", err)
	}
	return observation, nil
}

func (s *MongoStore) CompleteDecision(ctx context.Context, publicID, leaseToken, scopeState, decisionState string) error {
	if publicID == "" || leaseToken == "" || scopeState == "" || decisionState == "" {
		return ErrInvalidEnvelope
	}
	now := s.now().UTC()
	result, err := s.db.Collection(models.CollectionObservations).UpdateOne(ctx, bson.M{"public_id": publicID, "decision_state": "processing", "decision_lease_token": leaseToken, "decision_lease_expires_at": bson.M{"$gt": now}}, bson.M{
		"$set":   bson.M{"scope_state": scopeState, "decision_state": decisionState},
		"$unset": bson.M{"decision_lease_owner": "", "decision_lease_token": "", "decision_lease_expires_at": ""},
		"$inc":   bson.M{"version": 1},
	})
	if err != nil {
		return fmt.Errorf("complete observation decision: %w", err)
	}
	if result.ModifiedCount != 1 {
		return ErrNoPendingObservation
	}
	return nil
}

func (s *MongoStore) nextSequence(ctx context.Context, collection, id string, now time.Time) (int64, error) {
	after := options.After
	opts := options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(after)
	var counter models.Counter
	err := s.db.Collection(collection).FindOneAndUpdate(ctx, bson.M{"_id": id}, bson.M{
		"$inc": bson.M{"sequence": 1},
		"$set": bson.M{"updated_at": now},
	}, opts).Decode(&counter)
	return counter.Sequence, err
}

func (s *MongoStore) applyProjection(ctx context.Context, envelope types.SlackEnvelope, observation models.Observation) error {
	messageTS := projectionMessageTS(envelope)
	filter := bson.M{
		"organization_id": envelope.OrganizationID,
		"team_id":         envelope.TeamID,
		"channel_id":      envelope.ChannelID,
		"message_ts":      messageTS,
	}
	now := s.now().UTC()
	eventAt := observation.SlackEventTime.UTC()
	eventRank := projectionEventRank(envelope.Kind)
	text := envelope.Text
	deleted := false
	if envelope.Kind == types.SlackEventDelete {
		deleted = true
		text = ""
	}
	images := append([]types.SlackImageRef(nil), envelope.Images...)
	if deleted {
		images = nil
	}
	newer := bson.M{"$or": bson.A{
		bson.M{"$eq": bson.A{bson.M{"$type": "$source_event_at"}, "missing"}},
		bson.M{"$lt": bson.A{"$source_event_at", eventAt}},
		bson.M{"$and": bson.A{bson.M{"$eq": bson.A{"$source_event_at", eventAt}}, bson.M{"$lte": bson.A{"$source_event_rank", eventRank}}}},
	}}
	choose := func(next any, current string) bson.M { return bson.M{"$cond": bson.A{newer, next, current}} }
	update := mongo.Pipeline{{{Key: "$set", Value: bson.M{
		"organization_id":    bson.M{"$ifNull": bson.A{"$organization_id", envelope.OrganizationID}},
		"team_id":            bson.M{"$ifNull": bson.A{"$team_id", envelope.TeamID}},
		"channel_id":         bson.M{"$ifNull": bson.A{"$channel_id", envelope.ChannelID}},
		"message_ts":         bson.M{"$ifNull": bson.A{"$message_ts", messageTS}},
		"root_thread_ts":     bson.M{"$ifNull": bson.A{"$root_thread_ts", envelope.RootThreadTS()}},
		"author_id":          bson.M{"$ifNull": bson.A{"$author_id", envelope.UserID}},
		"bot_id":             bson.M{"$ifNull": bson.A{"$bot_id", envelope.BotID}},
		"subtype":            choose(envelope.Subtype, "$subtype"),
		"original_at":        bson.M{"$ifNull": bson.A{"$original_at", observation.SlackEventTime}},
		"source_event_id":    choose(envelope.EventID, "$source_event_id"),
		"source_event_at":    choose(eventAt, "$source_event_at"),
		"source_event_rank":  choose(eventRank, "$source_event_rank"),
		"updated_at":         choose(now, "$updated_at"),
		"restricted":         choose(envelope.Restricted, "$restricted"),
		"deleted":            choose(deleted, "$deleted"),
		"text":               choose(text, "$text"),
		"images":             choose(images, "$images"),
		"projection_version": bson.M{"$cond": bson.A{newer, bson.M{"$add": bson.A{bson.M{"$ifNull": bson.A{"$projection_version", 0}}, 1}}, "$projection_version"}},
	}}}, {{Key: "$unset", Value: "expires_at"}}}
	if _, err := s.db.Collection(models.CollectionMessages).UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(true)); err != nil {
		return fmt.Errorf("apply message projection: %w", err)
	}
	return nil
}

func (s *MongoStore) SetRestricted(ctx context.Context, publicID string, restricted bool) error {
	var observation models.Observation
	if err := s.db.Collection(models.CollectionObservations).FindOne(ctx, bson.M{"public_id": publicID}).Decode(&observation); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return ErrMessageNotFound
		}
		return err
	}
	if _, err := s.db.Collection(models.CollectionObservations).UpdateOne(ctx, bson.M{"public_id": publicID}, bson.M{"$set": bson.M{"restricted": restricted}, "$inc": bson.M{"version": 1}}); err != nil {
		return err
	}
	messageTS := observation.MessageTS
	if observation.MutationTargetTS != "" {
		messageTS = observation.MutationTargetTS
	}
	_, err := s.db.Collection(models.CollectionMessages).UpdateOne(ctx, bson.M{"organization_id": observation.OrganizationID, "team_id": observation.TeamID, "channel_id": observation.ChannelID, "message_ts": messageTS}, bson.M{"$set": bson.M{"restricted": restricted, "updated_at": s.now().UTC()}, "$inc": bson.M{"projection_version": 1}})
	return err
}

func (s *MongoStore) Recent(ctx context.Context, organizationID string, channelIDs []string, since time.Time, limit int) ([]models.ChannelMessage, error) {
	if limit <= 0 || len(channelIDs) == 0 {
		return nil, nil
	}
	filter := bson.M{
		"organization_id": organizationID,
		"channel_id":      bson.M{"$in": channelIDs},
		"deleted":         false,
		"original_at":     bson.M{"$gte": since},
	}
	cursor, err := s.db.Collection(models.CollectionMessages).Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "original_at", Value: -1}, {Key: "message_ts", Value: -1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, fmt.Errorf("find recent messages: %w", err)
	}
	defer cursor.Close(ctx)
	var messages []models.ChannelMessage
	if err := cursor.All(ctx, &messages); err != nil {
		return nil, fmt.Errorf("decode recent messages: %w", err)
	}
	for left, right := 0, len(messages)-1; left < right; left, right = left+1, right-1 {
		messages[left], messages[right] = messages[right], messages[left]
	}
	return messages, nil
}

func (s *MongoStore) CurrentMessage(ctx context.Context, organizationID, teamID, channelID, messageTS string) (models.ChannelMessage, error) {
	filter := bson.M{
		"organization_id": organizationID,
		"team_id":         teamID,
		"channel_id":      channelID,
		"message_ts":      messageTS,
	}
	var message models.ChannelMessage
	if err := s.db.Collection(models.CollectionMessages).FindOne(ctx, filter).Decode(&message); err != nil {
		if err == mongo.ErrNoDocuments {
			return models.ChannelMessage{}, ErrMessageNotFound
		}
		return models.ChannelMessage{}, fmt.Errorf("get message: %w", err)
	}
	return message, nil
}

func (s *MongoStore) Channels(ctx context.Context, organizationID string) ([]string, error) {
	var values []string
	err := s.db.Collection(models.CollectionMessages).Distinct(ctx, "channel_id", bson.M{
		"organization_id": organizationID,
	}).Decode(&values)
	if err != nil {
		return nil, fmt.Errorf("list observed channels: %w", err)
	}
	sort.Strings(values)
	return values, nil
}

func (s *MongoStore) ReserveOutput(ctx context.Context, observationID, reservationID string) (bool, error) {
	if observationID == "" || reservationID == "" {
		return false, ErrInvalidEnvelope
	}
	result, err := s.db.Collection(models.CollectionObservations).UpdateOne(ctx, bson.M{
		"public_id": observationID,
		"$or": bson.A{
			bson.M{"output_produced": false},
			bson.M{"output_produced": true, "output_reservation_id": reservationID},
		},
	}, bson.M{
		"$set": bson.M{"output_produced": true, "output_reservation_id": reservationID},
		"$inc": bson.M{"version": 1},
	})
	if err != nil {
		return false, fmt.Errorf("reserve observation output: %w", err)
	}
	return result.MatchedCount == 1, nil
}

func (s *MongoStore) FinalizeOutput(ctx context.Context, observationID, reservationID, jobID, deliveryID string) error {
	if observationID == "" || reservationID == "" || (jobID == "") == (deliveryID == "") {
		return ErrInvalidEnvelope
	}
	result, err := s.db.Collection(models.CollectionObservations).UpdateOne(ctx, bson.M{
		"public_id": observationID, "output_produced": true, "output_reservation_id": reservationID,
	}, bson.M{
		"$set":   bson.M{"output_job_id": jobID, "output_delivery_id": deliveryID},
		"$unset": bson.M{"output_reservation_id": ""},
		"$inc":   bson.M{"version": 1},
	})
	if err != nil {
		return fmt.Errorf("finalize observation output: %w", err)
	}
	if result.ModifiedCount != 1 {
		return ErrNoPendingObservation
	}
	return nil
}

func (s *MongoStore) LateCandidates(ctx context.Context, organizationID string, since, before time.Time, limit int) ([]models.Observation, error) {
	if organizationID == "" || limit <= 0 {
		return nil, nil
	}
	cursor, err := s.db.Collection(models.CollectionObservations).Find(ctx, bson.M{"organization_id": organizationID, "decision_state": "decided", "output_produced": false, "event_type": string(types.SlackEventMessage), "slack_event_time": bson.M{"$gte": since, "$lt": before}, "expires_at": bson.M{"$gt": s.now().UTC()}}, options.Find().SetSort(bson.D{{Key: "slack_event_time", Value: -1}}).SetLimit(int64(limit*4)))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var candidates []models.Observation
	if err := cursor.All(ctx, &candidates); err != nil {
		return nil, err
	}
	result := candidates[:0]
	for _, candidate := range candidates {
		if isStatusQuestion(candidate.Text) {
			result = append(result, candidate)
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}
