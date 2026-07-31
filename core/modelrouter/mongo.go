package modelrouter

import (
	"context"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/telemetryos/tos-tag/core/database"
	"github.com/telemetryos/tos-tag/models"
	"github.com/telemetryos/tos-tag/types"
)

type MongoStore struct{ db *database.Database }

func NewMongoStore(db *database.Database) *MongoStore { return &MongoStore{db: db} }

func (s *MongoStore) Load(ctx context.Context) ([]types.ModelProfile, []Rule, error) {
	profileCursor, err := s.db.Collection(models.CollectionModelProfiles).Find(ctx, bson.M{})
	if err != nil {
		return nil, nil, err
	}
	defer profileCursor.Close(ctx)
	var profiles []types.ModelProfile
	if err := profileCursor.All(ctx, &profiles); err != nil {
		return nil, nil, err
	}
	ruleCursor, err := s.db.Collection(models.CollectionModelRules).Find(ctx, bson.M{})
	if err != nil {
		return nil, nil, err
	}
	defer ruleCursor.Close(ctx)
	var rules []Rule
	if err := ruleCursor.All(ctx, &rules); err != nil {
		return nil, nil, err
	}
	return profiles, rules, nil
}

func (s *MongoStore) PutProfile(ctx context.Context, profile types.ModelProfile) error {
	document := bson.M{"organization_id": "", "id": profile.ID, "provider_id": profile.ProviderID, "model_id": profile.ModelID, "variant": profile.Variant, "provider_options": profile.ProviderOptions, "required_capabilities": profile.RequiredCapabilities, "allowed_data_classes": profile.AllowedDataClasses, "fallback_profile_ids": profile.FallbackProfileIDs, "max_input_tokens": profile.MaxInputTokens, "max_output_tokens": profile.MaxOutputTokens, "enabled": profile.Enabled}
	_, err := s.db.Collection(models.CollectionModelProfiles).ReplaceOne(ctx, bson.M{"organization_id": "", "id": profile.ID}, document, options.Replace().SetUpsert(true))
	return err
}

func (s *MongoStore) PutRule(ctx context.Context, rule Rule) error {
	_, err := s.db.Collection(models.CollectionModelRules).ReplaceOne(ctx, bson.M{"organization_id": rule.OrganizationID, "id": rule.ID}, rule, options.Replace().SetUpsert(true))
	return err
}

var _ Store = (*MongoStore)(nil)
