// Package intelligence maintains source-linked organization situation facts.
package intelligence

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/telemetryos/tos-tag/core/database"
	"github.com/telemetryos/tos-tag/models"
	"github.com/telemetryos/tos-tag/types"
)

type Result struct {
	Watermark int64
	FactIDs   []string
}
type Status struct {
	OrganizationID string    `json:"organization_id"`
	Watermark      int64     `json:"watermark"`
	LatestSequence int64     `json:"latest_sequence"`
	Lag            int64     `json:"lag"`
	UpdatedAt      time.Time `json:"updated_at,omitempty"`
}
type Projector interface {
	Project(context.Context, models.Observation) (Result, error)
}

type Mongo struct {
	db              *database.Database
	contextLookback time.Duration
	now             func() time.Time
}

func NewMongo(db *database.Database, contextLookback ...time.Duration) *Mongo {
	lookback := 30 * 24 * time.Hour
	if len(contextLookback) > 0 && contextLookback[0] > 0 {
		lookback = contextLookback[0]
	}
	return &Mongo{db: db, contextLookback: lookback, now: time.Now}
}

// Recall returns only destination-safe organization facts. Restricted signals
// intentionally have no equivalent recall path outside their source channel.
func (p *Mongo) Recall(ctx context.Context, organizationID string, now time.Time, limit int) ([]models.SituationFact, error) {
	if organizationID == "" {
		return nil, fmt.Errorf("organization ID is required")
	}
	if limit <= 0 || limit > 100 {
		limit = 40
	}
	cursor, err := p.db.Collection(models.CollectionSituationFacts).Find(ctx, bson.M{"organization_id": organizationID, "status": "active", "expires_at": bson.M{"$gt": now.UTC()}}, options.Find().SetSort(bson.D{{Key: "updated_at", Value: -1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var facts []models.SituationFact
	return facts, cursor.All(ctx, &facts)
}
func (p *Mongo) Status(ctx context.Context, organizationID string) (Status, error) {
	var watermark models.ProjectorWatermark
	err := p.db.Collection(models.CollectionProjectorWatermarks).FindOne(ctx, bson.M{"_id": organizationID}).Decode(&watermark)
	if errors.Is(err, mongo.ErrNoDocuments) {
		err = nil
	}
	if err != nil {
		return Status{}, err
	}
	var counter models.Counter
	err = p.db.Collection(models.CollectionOrganizationCounts).FindOne(ctx, bson.M{"_id": organizationID}).Decode(&counter)
	if errors.Is(err, mongo.ErrNoDocuments) {
		err = nil
	}
	if err != nil {
		return Status{}, err
	}
	lag := counter.Sequence - watermark.Sequence
	if lag < 0 {
		lag = 0
	}
	return Status{OrganizationID: organizationID, Watermark: watermark.Sequence, LatestSequence: counter.Sequence, Lag: lag, UpdatedAt: watermark.UpdatedAt}, nil
}

func (p *Mongo) Project(ctx context.Context, observation models.Observation) (Result, error) {
	if observation.OrganizationID == "" || observation.ChannelID == "" || observation.PublicID == "" {
		return Result{}, fmt.Errorf("observation scope is required")
	}
	messageTS := observation.MessageTS
	if observation.MutationTargetTS != "" {
		messageTS = observation.MutationTargetTS
	}
	sourceFilter := bson.M{"organization_id": observation.OrganizationID, "channel_id": observation.ChannelID, "message_ts": messageTS}
	if requiresProjectionCleanup(observation) {
		if _, err := p.db.Collection(models.CollectionSituationFacts).DeleteMany(ctx, sourceFilter); err != nil {
			return Result{}, err
		}
		if _, err := p.db.Collection(models.CollectionRestrictedSignals).DeleteMany(ctx, sourceFilter); err != nil {
			return Result{}, err
		}
		if _, err := p.db.Collection(models.CollectionDerivations).DeleteMany(ctx, bson.M{"organization_id": observation.OrganizationID, "source_id": observation.PublicID}); err != nil {
			return Result{}, err
		}
	}
	count, err := p.db.Collection(models.CollectionChannels).CountDocuments(ctx, bson.M{"organization_id": observation.OrganizationID, "team_id": observation.TeamID, "channel_id": observation.ChannelID, "context_history_mode": string(types.ContextHistorySessionOnly)})
	if err != nil {
		return Result{}, err
	}
	if count > 0 {
		return p.advance(ctx, observation, nil)
	}
	if observation.EventType == string(types.SlackEventDelete) {
		return p.advance(ctx, observation, nil)
	}
	var message models.ChannelMessage
	err = p.db.Collection(models.CollectionMessages).FindOne(ctx, bson.M{"organization_id": observation.OrganizationID, "team_id": observation.TeamID, "channel_id": observation.ChannelID, "message_ts": messageTS, "deleted": false}).Decode(&message)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return p.advance(ctx, observation, nil)
	}
	if err != nil {
		return Result{}, err
	}
	now := p.now().UTC()
	sourceExpiresAt := message.OriginalAt.Add(p.contextLookback)
	if !sourceExpiresAt.After(now) || !isIncident(message.Text) {
		return p.advance(ctx, observation, nil)
	}
	var derivedID string
	if message.Restricted {
		doc := models.RestrictedSignal{PublicID: types.NewID("signal"), OrganizationID: observation.OrganizationID, Kind: "active_incident", Active: true, SourceID: observation.PublicID, ChannelID: observation.ChannelID, MessageTS: messageTS, CreatedAt: now, ExpiresAt: sourceExpiresAt}
		var saved models.RestrictedSignal
		err = p.db.Collection(models.CollectionRestrictedSignals).FindOneAndUpdate(ctx, bson.M{"organization_id": observation.OrganizationID, "channel_id": observation.ChannelID, "message_ts": messageTS, "kind": doc.Kind}, bson.M{"$set": bson.M{"active": true, "source_id": observation.PublicID, "expires_at": sourceExpiresAt}, "$setOnInsert": bson.M{"public_id": doc.PublicID, "organization_id": doc.OrganizationID, "kind": doc.Kind, "channel_id": doc.ChannelID, "message_ts": doc.MessageTS, "created_at": doc.CreatedAt}}, options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)).Decode(&saved)
		derivedID = saved.PublicID
	} else {
		doc := models.SituationFact{PublicID: types.NewID("fact"), OrganizationID: observation.OrganizationID, Kind: "active_incident", Status: "active", Summary: safeSummary(message.Text), SourceIDs: []string{observation.PublicID}, ChannelID: observation.ChannelID, MessageTS: messageTS, SourceExpiresAt: sourceExpiresAt, UpdatedAt: now, ExpiresAt: sourceExpiresAt}
		var saved models.SituationFact
		err = p.db.Collection(models.CollectionSituationFacts).FindOneAndUpdate(ctx, bson.M{"organization_id": observation.OrganizationID, "channel_id": observation.ChannelID, "message_ts": messageTS, "kind": doc.Kind}, bson.M{"$set": doc}, options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)).Decode(&saved)
		derivedID = saved.PublicID
	}
	if err != nil {
		return Result{}, err
	}
	link := models.SourceDerivation{OrganizationID: observation.OrganizationID, SourceID: observation.PublicID, DerivedCollection: models.CollectionSituationFacts, DerivedID: derivedID, CreatedAt: now, ExpiresAt: sourceExpiresAt}
	if message.Restricted {
		link.DerivedCollection = models.CollectionRestrictedSignals
	}
	_, err = p.db.Collection(models.CollectionDerivations).UpdateOne(ctx, bson.M{"organization_id": link.OrganizationID, "source_id": link.SourceID, "derived_collection": link.DerivedCollection, "derived_id": link.DerivedID}, bson.M{"$setOnInsert": link}, options.UpdateOne().SetUpsert(true))
	if err != nil {
		return Result{}, err
	}
	return p.advance(ctx, observation, []string{derivedID})
}

func requiresProjectionCleanup(observation models.Observation) bool {
	return observation.MutationTargetTS != "" || observation.EventType == string(types.SlackEventEdit) || observation.EventType == string(types.SlackEventDelete)
}

func (p *Mongo) advance(ctx context.Context, o models.Observation, ids []string) (Result, error) {
	now := p.now().UTC()
	after := options.After
	var watermark models.ProjectorWatermark
	err := p.db.Collection(models.CollectionProjectorWatermarks).FindOneAndUpdate(ctx, bson.M{"_id": o.OrganizationID, "$or": bson.A{bson.M{"sequence": bson.M{"$lt": o.OrganizationReceivedSeq}}, bson.M{"sequence": bson.M{"$exists": false}}}}, bson.M{"$set": bson.M{"organization_id": o.OrganizationID, "sequence": o.OrganizationReceivedSeq, "observed_at": o.SlackEventTime, "updated_at": now}}, options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(after)).Decode(&watermark)
	if mongo.IsDuplicateKeyError(err) {
		err = p.db.Collection(models.CollectionProjectorWatermarks).FindOne(ctx, bson.M{"_id": o.OrganizationID}).Decode(&watermark)
	}
	if err != nil {
		return Result{}, err
	}
	return Result{Watermark: watermark.Sequence, FactIDs: ids}, nil
}
func isIncident(text string) bool {
	value := strings.ToLower(text)
	return strings.Contains(value, "incident") || strings.Contains(value, "outage") || strings.Contains(value, "system is down") || strings.Contains(value, "api is down")
}
func safeSummary(text string) string {
	value := strings.TrimSpace(text)
	if len(value) > 500 {
		return value[:500]
	}
	return value
}
