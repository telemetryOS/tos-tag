package admission

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/telemetryos/tos-tag/core/database"
	"github.com/telemetryos/tos-tag/core/orgconfig"
	"github.com/telemetryos/tos-tag/models"
	"github.com/telemetryos/tos-tag/types"
)

type Mongo struct {
	db  *database.Database
	now func() time.Time
}

type mongoState struct {
	ID        string    `bson:"_id"`
	Hour      time.Time `bson:"hour"`
	Responses int       `bson:"responses"`
	Active    int       `bson:"active"`
	Last      time.Time `bson:"last"`
}

type mongoReservation struct {
	ID        string    `bson:"_id"`
	StateID   string    `bson:"state_id"`
	Completed bool      `bson:"completed"`
	ExpiresAt time.Time `bson:"expires_at"`
}

func NewMongo(db *database.Database) *Mongo { return &Mongo{db: db, now: time.Now} }

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
	var reservation mongoReservation
	err := m.db.Collection(models.CollectionAdmissionReservations).FindOneAndUpdate(ctx,
		bson.M{"_id": id, "completed": false},
		bson.M{"$set": bson.M{"completed": true, "completed_at": m.now().UTC(), "cleanup_at": m.now().UTC().Add(24 * time.Hour)}},
		options.FindOneAndUpdate().SetReturnDocument(options.Before),
	).Decode(&reservation)
	if err == nil {
		_, _ = m.db.Collection(models.CollectionAdmissionStates).UpdateOne(ctx, bson.M{"_id": reservation.StateID, "active": bson.M{"$gt": 0}}, bson.M{"$inc": bson.M{"active": -1}})
	}
}

func (m *Mongo) reconcileExpired(ctx context.Context, stateID string, now time.Time) error {
	result, err := m.db.Collection(models.CollectionAdmissionReservations).UpdateMany(ctx,
		bson.M{"state_id": stateID, "completed": false, "expires_at": bson.M{"$lte": now}},
		bson.M{"$set": bson.M{"completed": true, "completed_at": now, "cleanup_at": now.Add(24 * time.Hour)}},
	)
	if err != nil || result.ModifiedCount == 0 {
		return err
	}
	_, err = m.db.Collection(models.CollectionAdmissionStates).UpdateOne(ctx, bson.M{"_id": stateID}, bson.M{"$inc": bson.M{"active": -result.ModifiedCount}})
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
