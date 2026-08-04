package memory

import (
	"context"
	"errors"
	"strings"
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

func NewMongoStore(db *database.Database) *MongoStore { return &MongoStore{db: db, now: time.Now} }

func (s *MongoStore) List(ctx context.Context, organizationID string, limit int) ([]Record, error) {
	if strings.TrimSpace(organizationID) == "" {
		return nil, errors.New("organization ID is required")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	cursor, err := s.db.Collection(models.CollectionSummaries).Find(ctx, bson.M{"organization_id": organizationID}, options.Find().SetSort(bson.D{{Key: "pinned", Value: -1}, {Key: "updated_at", Value: -1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var records []Record
	return records, cursor.All(ctx, &records)
}

func (s *MongoStore) Recall(ctx context.Context, organizationID, channelID, rootThreadTS string, now time.Time, limit int) ([]Record, error) {
	if organizationID == "" || channelID == "" {
		return nil, errors.New("memory recall scope is required")
	}
	if limit <= 0 || limit > 100 {
		limit = 40
	}
	filter := bson.M{
		"organization_id": organizationID,
		"status":          StatusActive,
		"$or": bson.A{
			bson.M{"pinned": true},
			bson.M{"expires_at": bson.M{"$gt": now.UTC()}},
		},
		"$and": bson.A{
			bson.M{"$or": bson.A{bson.M{"restricted": false}, bson.M{"channel_id": channelID}}},
			bson.M{"$or": bson.A{bson.M{"scope": ScopeChannel}, bson.M{"channel_id": channelID, "root_thread_ts": rootThreadTS}}},
			bson.M{"$or": bson.A{bson.M{"origin": "operator"}, bson.M{"facts.0": bson.M{"$exists": true}}}},
		},
	}
	cursor, err := s.db.Collection(models.CollectionSummaries).Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "pinned", Value: -1}, {Key: "updated_at", Value: -1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var records []Record
	return records, cursor.All(ctx, &records)
}

func (s *MongoStore) FindScope(ctx context.Context, organizationID, scopeKey string) (Record, error) {
	var record Record
	err := s.db.Collection(models.CollectionSummaries).FindOne(ctx, bson.M{"organization_id": organizationID, "scope_key": scopeKey}).Decode(&record)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Record{}, ErrNotFound
	}
	return record, err
}

func (s *MongoStore) PutGenerated(ctx context.Context, record Record) (Record, bool, error) {
	if record.OrganizationID == "" || record.ChannelID == "" || record.ScopeKey == "" || record.SourceHash == "" || record.Status != StatusActive {
		return Record{}, false, errors.New("generated memory scope and source hash are required")
	}
	text, err := validateText(record.Text)
	if err != nil {
		return Record{}, false, err
	}
	record.Text = text
	for attempt := 0; attempt < 8; attempt++ {
		existing, findErr := s.FindScope(ctx, record.OrganizationID, record.ScopeKey)
		if findErr != nil && !errors.Is(findErr, ErrNotFound) {
			return Record{}, false, findErr
		}
		if findErr == nil && (existing.SourceHash == record.SourceHash || (existing.Status == StatusActive && (existing.Pinned || existing.Origin == "operator"))) {
			return existing, false, nil
		}

		now := s.now().UTC()
		candidate := record
		candidate.UpdatedAt = now
		candidate.Origin = "model"
		if errors.Is(findErr, ErrNotFound) {
			if candidate.ID == "" {
				candidate.ID = types.NewID("mem")
			}
			if candidate.CreatedAt.IsZero() {
				candidate.CreatedAt = now
			}
			candidate.Revision = 1
			if _, insertErr := s.db.Collection(models.CollectionSummaries).InsertOne(ctx, candidate); insertErr == nil {
				return candidate, true, nil
			} else if mongo.IsDuplicateKeyError(insertErr) {
				continue
			} else {
				return Record{}, false, insertErr
			}
		}

		candidate.ID = existing.ID
		candidate.CreatedAt = existing.CreatedAt
		candidate.Revision = existing.Revision + 1
		var saved Record
		updateErr := s.db.Collection(models.CollectionSummaries).FindOneAndUpdate(ctx,
			bson.M{"organization_id": record.OrganizationID, "scope_key": record.ScopeKey, "revision": existing.Revision},
			bson.M{"$set": candidate},
			options.FindOneAndUpdate().SetReturnDocument(options.After),
		).Decode(&saved)
		if errors.Is(updateErr, mongo.ErrNoDocuments) {
			continue
		}
		if updateErr != nil {
			return Record{}, false, updateErr
		}
		return saved, true, nil
	}
	return Record{}, false, errors.New("generated memory update conflicted repeatedly")
}

func (s *MongoStore) Correct(ctx context.Context, organizationID, recordID, text, actorID string) (Record, error) {
	text, err := validateText(text)
	if err != nil || organizationID == "" || recordID == "" || strings.TrimSpace(actorID) == "" {
		return Record{}, errors.New("valid organization, record, text, and actor are required")
	}
	var saved Record
	err = s.db.Collection(models.CollectionSummaries).FindOneAndUpdate(ctx, bson.M{"organization_id": organizationID, "public_id": recordID}, bson.M{
		"$set":   bson.M{"text": text, "origin": "operator", "status": StatusActive, "pinned": true, "updated_at": s.now().UTC(), "corrected_by": actorID},
		"$unset": bson.M{"expires_at": ""},
		"$inc":   bson.M{"revision": 1},
	}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&saved)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Record{}, ErrNotFound
	}
	return saved, err
}

func (s *MongoStore) SetPinned(ctx context.Context, organizationID, recordID string, pinned bool, actorID string) (Record, error) {
	if organizationID == "" || recordID == "" || strings.TrimSpace(actorID) == "" {
		return Record{}, errors.New("valid organization, record, and actor are required")
	}
	set := bson.M{"pinned": pinned, "updated_at": s.now().UTC(), "pinned_by": actorID}
	update := bson.M{"$set": set, "$inc": bson.M{"revision": 1}}
	if pinned {
		update["$unset"] = bson.M{"expires_at": ""}
	} else {
		var current Record
		if err := s.db.Collection(models.CollectionSummaries).FindOne(ctx, bson.M{"organization_id": organizationID, "public_id": recordID}).Decode(&current); err != nil {
			if errors.Is(err, mongo.ErrNoDocuments) {
				return Record{}, ErrNotFound
			}
			return Record{}, err
		}
		if !current.NaturalExpiresAt.IsZero() {
			set["expires_at"] = current.NaturalExpiresAt
		}
	}
	var saved Record
	err := s.db.Collection(models.CollectionSummaries).FindOneAndUpdate(ctx, bson.M{"organization_id": organizationID, "public_id": recordID}, update, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&saved)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Record{}, ErrNotFound
	}
	return saved, err
}

func (s *MongoStore) Forget(ctx context.Context, organizationID, recordID, actorID string) (Record, error) {
	if organizationID == "" || recordID == "" || strings.TrimSpace(actorID) == "" {
		return Record{}, errors.New("valid organization, record, and actor are required")
	}
	var saved Record
	err := s.db.Collection(models.CollectionSummaries).FindOneAndUpdate(ctx, bson.M{"organization_id": organizationID, "public_id": recordID}, bson.M{
		"$set":   bson.M{"status": StatusForgotten, "pinned": false, "origin": "operator", "forgotten_by": actorID, "updated_at": s.now().UTC()},
		"$unset": bson.M{"text": "", "facts": "", "source_ids": "", "model": "", "reasoning_effort": "", "expires_at": "", "natural_expires_at": ""},
		"$inc":   bson.M{"revision": 1},
	}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&saved)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Record{}, ErrNotFound
	}
	return saved, err
}
