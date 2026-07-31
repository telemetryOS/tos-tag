package contextpacks

import (
	"context"
	"errors"
	"fmt"
	"github.com/telemetryos/tos-tag/core/database"
	"github.com/telemetryos/tos-tag/models"
	"github.com/telemetryos/tos-tag/types"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type Store interface {
	Save(context.Context, types.ContextPackRevision, int64) error
	Get(context.Context, string, string, int64) (types.ContextPackRevision, error)
}
type MongoStore struct{ db *database.Database }

func NewMongoStore(db *database.Database) *MongoStore { return &MongoStore{db: db} }
func (s *MongoStore) Save(ctx context.Context, pack types.ContextPackRevision, revision int64) error {
	if pack.ID == "" || pack.OrganizationID == "" || pack.TargetObservationID == "" || revision <= 0 || pack.ExpiresAt.IsZero() {
		return fmt.Errorf("invalid context pack")
	}
	sourceIDs := make([]string, 0, len(pack.Sources))
	for _, source := range pack.Sources {
		sourceIDs = append(sourceIDs, source.ID)
	}
	doc := models.ContextPack{PublicID: string(pack.ID), OrganizationID: pack.OrganizationID, TargetObservationID: pack.TargetObservationID, Revision: revision, Payload: pack, SourceIDs: sourceIDs, CreatedAt: pack.CreatedAt, ExpiresAt: pack.ExpiresAt}
	_, err := s.db.Collection(models.CollectionContextPacks).UpdateOne(ctx, bson.M{"organization_id": pack.OrganizationID, "target_observation_id": pack.TargetObservationID, "revision": revision}, bson.M{"$setOnInsert": doc}, options.UpdateOne().SetUpsert(true))
	return err
}
func (s *MongoStore) Get(ctx context.Context, org, target string, revision int64) (types.ContextPackRevision, error) {
	var doc models.ContextPack
	err := s.db.Collection(models.CollectionContextPacks).FindOne(ctx, bson.M{"organization_id": org, "target_observation_id": target, "revision": revision}).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return types.ContextPackRevision{}, fmt.Errorf("context pack not found")
	}
	if err != nil {
		return types.ContextPackRevision{}, err
	}
	encoded, err := bson.Marshal(doc.Payload)
	if err != nil {
		return types.ContextPackRevision{}, err
	}
	var pack types.ContextPackRevision
	err = bson.Unmarshal(encoded, &pack)
	return pack, err
}
