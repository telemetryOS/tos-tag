package sessions

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

type MongoStore struct {
	db  *database.Database
	now func() time.Time
}

func NewMongoStore(db *database.Database) *MongoStore {
	return &MongoStore{db: db, now: time.Now}
}

func (s *MongoStore) Resolve(ctx context.Context, organizationID, teamID, channelID, rootThreadTS string) (Session, bool, error) {
	if organizationID == "" || teamID == "" || channelID == "" || rootThreadTS == "" {
		return Session{}, false, fmt.Errorf("session scope is required")
	}
	filter := bson.M{"organization_id": organizationID, "team_id": teamID, "channel_id": channelID, "root_thread_ts": rootThreadTS}
	var existing models.Session
	if err := s.db.Collection(models.CollectionSessions).FindOne(ctx, filter).Decode(&existing); err == nil {
		return sessionFromModel(existing), false, nil
	} else if !errors.Is(err, mongo.ErrNoDocuments) {
		return Session{}, false, fmt.Errorf("get session: %w", err)
	}
	now := s.now().UTC()
	doc := models.Session{PublicID: types.NewID("ses"), OrganizationID: organizationID, TeamID: teamID, ChannelID: channelID, RootThreadTS: rootThreadTS, CurrentGeneration: 1, CreatedAt: now, UpdatedAt: now, Version: 1}
	if _, err := s.db.Collection(models.CollectionSessions).InsertOne(ctx, doc); err != nil {
		if !mongo.IsDuplicateKeyError(err) {
			return Session{}, false, fmt.Errorf("create session: %w", err)
		}
		if err := s.db.Collection(models.CollectionSessions).FindOne(ctx, filter).Decode(&existing); err != nil {
			return Session{}, false, fmt.Errorf("resolve duplicate session: %w", err)
		}
		return sessionFromModel(existing), false, nil
	}
	return sessionFromModel(doc), true, nil
}

func (s *MongoStore) Restart(ctx context.Context, organizationID, teamID, channelID, rootThreadTS string) (Session, error) {
	after := options.After
	var updated models.Session
	err := s.db.Collection(models.CollectionSessions).FindOneAndUpdate(ctx, bson.M{"organization_id": organizationID, "team_id": teamID, "channel_id": channelID, "root_thread_ts": rootThreadTS}, bson.M{
		"$inc": bson.M{"current_generation": 1, "version": 1}, "$set": bson.M{"updated_at": s.now().UTC()},
	}, options.FindOneAndUpdate().SetReturnDocument(after)).Decode(&updated)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return Session{}, fmt.Errorf("session not found")
		}
		return Session{}, fmt.Errorf("restart session: %w", err)
	}
	return sessionFromModel(updated), nil
}

func (s *MongoStore) Find(ctx context.Context, organizationID, teamID, channelID, rootThreadTS string) (Session, error) {
	var doc models.Session
	err := s.db.Collection(models.CollectionSessions).FindOne(ctx, bson.M{"organization_id": organizationID, "team_id": teamID, "channel_id": channelID, "root_thread_ts": rootThreadTS}).Decode(&doc)
	if err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return Session{}, fmt.Errorf("session not found")
		}
		return Session{}, fmt.Errorf("find session: %w", err)
	}
	return sessionFromModel(doc), nil
}

func sessionFromModel(doc models.Session) Session {
	return Session{ID: types.SessionID(doc.PublicID), OrganizationID: doc.OrganizationID, TeamID: doc.TeamID, ChannelID: doc.ChannelID, RootThreadTS: doc.RootThreadTS, CurrentGeneration: doc.CurrentGeneration, CreatedAt: doc.CreatedAt, UpdatedAt: doc.UpdatedAt}
}
