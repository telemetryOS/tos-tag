package admission

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/RobertWHurst/blackbox"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/telemetryos/tos-tag/core/database"
	"github.com/telemetryos/tos-tag/core/orgconfig"
	"github.com/telemetryos/tos-tag/models"
	"github.com/telemetryos/tos-tag/types"
)

type Mongo struct {
	db     *database.Database
	now    func() time.Time
	logger *blackbox.Logger
}

const (
	releaseStatePending   = "pending"
	releaseStateReleasing = "releasing"
	releaseStateReleased  = "released"
	releaseLeaseDuration  = 30 * time.Second
)

type mongoState struct {
	ID             string    `bson:"_id"`
	Hour           time.Time `bson:"hour"`
	Responses      int       `bson:"responses"`
	Active         int       `bson:"active"`
	Last           time.Time `bson:"last"`
	ReleaseMarkers []string  `bson:"release_markers,omitempty"`
}

type mongoReservation struct {
	ID                    string    `bson:"_id"`
	StateID               string    `bson:"state_id"`
	Completed             bool      `bson:"completed"`
	ExpiresAt             time.Time `bson:"expires_at"`
	ReleaseState          string    `bson:"release_state,omitempty"`
	ReleaseToken          string    `bson:"release_token,omitempty"`
	ReleaseLeaseExpiresAt time.Time `bson:"release_lease_expires_at,omitempty"`
}

func NewMongo(db *database.Database, loggers ...*blackbox.Logger) *Mongo {
	logger := blackbox.New()
	if len(loggers) > 0 && loggers[0] != nil {
		logger = loggers[0]
	}
	return &Mongo{db: db, now: time.Now, logger: logger}
}

func (m *Mongo) Admit(ctx context.Context, policy orgconfig.ChannelPolicy) (string, error) {
	if err := orgconfig.ValidateChannel(policy); err != nil {
		return "", err
	}
	if policy.KillSwitch || !policy.Enrolled {
		return "", ErrKillSwitch
	}
	now := m.now().UTC()
	stateID := policy.OrganizationID + "/" + policy.TeamID + "/" + policy.ChannelID
	if err := m.reconcileExpired(ctx, stateID, now); err != nil {
		return "", err
	}
	hour := now.Truncate(time.Hour)
	cutoff := now.Add(-policy.Cooldown)
	filter := bson.M{
		"_id": stateID,
		"$and": bson.A{
			bson.M{"$or": bson.A{bson.M{"active": bson.M{"$lt": policy.MaxConcurrentJobs}}, bson.M{"active": bson.M{"$exists": false}}}},
			bson.M{"$or": bson.A{bson.M{"hour": bson.M{"$ne": hour}}, bson.M{"responses": bson.M{"$lt": policy.MaxResponsesPerHour}}, bson.M{"responses": bson.M{"$exists": false}}}},
			bson.M{"$or": bson.A{bson.M{"last": bson.M{"$lte": cutoff}}, bson.M{"last": bson.M{"$exists": false}}}},
		},
	}
	update := mongo.Pipeline{bson.D{{Key: "$set", Value: bson.M{
		"organization_id": policy.OrganizationID,
		"team_id":         policy.TeamID,
		"channel_id":      policy.ChannelID,
		"hour":            hour,
		"responses": bson.M{"$cond": bson.A{
			bson.M{"$eq": bson.A{bson.M{"$ifNull": bson.A{"$hour", time.Time{}}}, hour}},
			bson.M{"$add": bson.A{bson.M{"$ifNull": bson.A{"$responses", 0}}, 1}},
			1,
		}},
		"active":     bson.M{"$add": bson.A{bson.M{"$ifNull": bson.A{"$active", 0}}, 1}},
		"last":       now,
		"updated_at": now,
	}}}}
	var updated mongoState
	err := m.db.Collection(models.CollectionAdmissionStates).FindOneAndUpdate(ctx, filter, update, options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)).Decode(&updated)
	if err != nil {
		if !errors.Is(err, mongo.ErrNoDocuments) && !mongo.IsDuplicateKeyError(err) {
			return "", err
		}
		return "", m.denialReason(ctx, stateID, policy, now)
	}
	reservationID := types.NewID("admit")
	_, err = m.db.Collection(models.CollectionAdmissionReservations).InsertOne(ctx, mongoReservation{ID: reservationID, StateID: stateID, ExpiresAt: now.Add(24 * time.Hour)})
	if err != nil {
		_, _ = m.db.Collection(models.CollectionAdmissionStates).UpdateOne(ctx, bson.M{"_id": stateID, "active": bson.M{"$gt": 0}}, bson.M{"$inc": bson.M{"active": -1}})
		return "", err
	}
	return reservationID, nil
}

func (m *Mongo) Complete(ctx context.Context, id string) {
	if id == "" {
		return
	}
	now := m.now().UTC()
	_, err := m.db.Collection(models.CollectionAdmissionReservations).UpdateOne(ctx,
		bson.M{"_id": id, "completed": false},
		bson.M{"$set": bson.M{"completed": true, "completed_at": now, "cleanup_at": now.Add(24 * time.Hour), "release_state": releaseStatePending}},
	)
	if err == nil {
		_, err = m.releaseReservation(ctx, id, now)
	}
	if err != nil {
		m.logger.WithCtx(blackbox.Ctx{"reservation_id": id, "error_type": fmt.Sprintf("%T", err)}).Error("admission reservation release deferred for reconciliation")
	}
}

func (m *Mongo) reconcileExpired(ctx context.Context, stateID string, now time.Time) error {
	_, err := m.db.Collection(models.CollectionAdmissionReservations).UpdateMany(ctx,
		bson.M{"state_id": stateID, "completed": false, "expires_at": bson.M{"$lte": now}},
		bson.M{"$set": bson.M{"completed": true, "completed_at": now, "cleanup_at": now.Add(24 * time.Hour), "release_state": releaseStatePending}},
	)
	if err != nil {
		return err
	}
	if err := m.reconcilePendingReleases(ctx, stateID, now); err != nil {
		return err
	}
	return m.cleanupReleaseMarkers(ctx, stateID)
}

func (m *Mongo) reconcilePendingReleases(ctx context.Context, stateID string, now time.Time) error {
	filter := bson.M{
		"state_id":  stateID,
		"completed": true,
		"$or": bson.A{
			bson.M{"release_state": releaseStatePending},
			bson.M{"release_state": releaseStateReleasing, "release_lease_expires_at": bson.M{"$lte": now}},
		},
	}
	for {
		var reservation mongoReservation
		err := m.db.Collection(models.CollectionAdmissionReservations).FindOne(ctx, filter).Decode(&reservation)
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil
		}
		if err != nil {
			return err
		}
		claimed, err := m.releaseReservation(ctx, reservation.ID, now)
		if err != nil {
			return err
		}
		if !claimed {
			// Another controller won the short release lease. Exclude its active
			// lease on the next query and continue repairing other reservations.
			continue
		}
	}
}

func (m *Mongo) releaseReservation(ctx context.Context, reservationID string, now time.Time) (bool, error) {
	token := types.NewID("release")
	claimFilter := bson.M{
		"_id":       reservationID,
		"completed": true,
		"$or": bson.A{
			bson.M{"release_state": releaseStatePending},
			bson.M{"release_state": releaseStateReleasing, "release_lease_expires_at": bson.M{"$lte": now}},
		},
	}
	var reservation mongoReservation
	err := m.db.Collection(models.CollectionAdmissionReservations).FindOneAndUpdate(ctx, claimFilter, bson.M{"$set": bson.M{
		"release_state":            releaseStateReleasing,
		"release_token":            token,
		"release_lease_expires_at": now.Add(releaseLeaseDuration),
	}}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&reservation)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return false, nil
	}
	if err != nil {
		return false, err
	}

	stateResult, err := m.db.Collection(models.CollectionAdmissionStates).UpdateOne(ctx,
		bson.M{"_id": reservation.StateID, "release_markers": bson.M{"$ne": reservation.ID}},
		mongo.Pipeline{bson.D{{Key: "$set", Value: bson.M{
			"active": bson.M{"$max": bson.A{
				bson.M{"$subtract": bson.A{bson.M{"$ifNull": bson.A{"$active", 0}}, 1}},
				0,
			}},
			"release_markers": bson.M{"$setUnion": bson.A{
				bson.M{"$ifNull": bson.A{"$release_markers", bson.A{}}},
				bson.A{reservation.ID},
			}},
			"updated_at": now,
		}}}},
	)
	if err != nil {
		return true, err
	}
	if stateResult.MatchedCount == 0 {
		markerCount, markerErr := m.db.Collection(models.CollectionAdmissionStates).CountDocuments(ctx, bson.M{"_id": reservation.StateID, "release_markers": reservation.ID}, options.Count().SetLimit(1))
		if markerErr != nil {
			return true, markerErr
		}
		if markerCount == 0 {
			return true, errors.New("admission state missing while releasing reservation")
		}
	}

	finalized, err := m.db.Collection(models.CollectionAdmissionReservations).UpdateOne(ctx,
		bson.M{"_id": reservation.ID, "release_state": releaseStateReleasing, "release_token": token},
		bson.M{
			"$set":   bson.M{"release_state": releaseStateReleased, "released_at": now},
			"$unset": bson.M{"release_token": "", "release_lease_expires_at": ""},
		},
	)
	if err != nil {
		return true, err
	}
	if finalized.MatchedCount == 0 {
		return true, nil
	}
	_, err = m.db.Collection(models.CollectionAdmissionStates).UpdateOne(ctx,
		bson.M{"_id": reservation.StateID},
		bson.M{"$pull": bson.M{"release_markers": reservation.ID}},
	)
	return true, err
}

func (m *Mongo) cleanupReleaseMarkers(ctx context.Context, stateID string) error {
	var state mongoState
	if err := m.db.Collection(models.CollectionAdmissionStates).FindOne(ctx, bson.M{"_id": stateID}).Decode(&state); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil
		}
		return err
	}
	if len(state.ReleaseMarkers) == 0 {
		return nil
	}
	cursor, err := m.db.Collection(models.CollectionAdmissionReservations).Find(ctx, bson.M{
		"_id":           bson.M{"$in": state.ReleaseMarkers},
		"release_state": bson.M{"$ne": releaseStateReleased},
	})
	if err != nil {
		return err
	}
	defer cursor.Close(ctx)
	unreleased := make(map[string]struct{})
	for cursor.Next(ctx) {
		var reservation mongoReservation
		if err := cursor.Decode(&reservation); err != nil {
			return err
		}
		unreleased[reservation.ID] = struct{}{}
	}
	if err := cursor.Err(); err != nil {
		return err
	}
	removable := make([]string, 0, len(state.ReleaseMarkers))
	for _, marker := range state.ReleaseMarkers {
		if _, ok := unreleased[marker]; !ok {
			removable = append(removable, marker)
		}
	}
	if len(removable) == 0 {
		return nil
	}
	_, err = m.db.Collection(models.CollectionAdmissionStates).UpdateOne(ctx,
		bson.M{"_id": stateID},
		bson.M{"$pull": bson.M{"release_markers": bson.M{"$in": removable}}},
	)
	return err
}

func (m *Mongo) denialReason(ctx context.Context, stateID string, policy orgconfig.ChannelPolicy, now time.Time) error {
	var current mongoState
	if err := m.db.Collection(models.CollectionAdmissionStates).FindOne(ctx, bson.M{"_id": stateID}).Decode(&current); err != nil {
		return err
	}
	if current.Active >= policy.MaxConcurrentJobs {
		return ErrConcurrency
	}
	if current.Hour.Equal(now.Truncate(time.Hour)) && current.Responses >= policy.MaxResponsesPerHour {
		return ErrBudget
	}
	if !current.Last.IsZero() && now.Sub(current.Last) < policy.Cooldown {
		return ErrCooldown
	}
	return ErrConcurrency
}

var _ Controller = (*Mongo)(nil)
