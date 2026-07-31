// Package management exposes allowlisted durable records to the admin plane.
package management

import (
	"context"
	"fmt"
	"github.com/telemetryos/tos-tag/core/database"
	"github.com/telemetryos/tos-tag/models"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type Reader interface {
	List(context.Context, string, string, int) ([]bson.M, error)
}
type Mongo struct{ db *database.Database }

func NewMongo(db *database.Database) *Mongo { return &Mongo{db: db} }

var allowed = map[string]string{"observations": models.CollectionObservations, "context": models.CollectionContextPacks, "audit": models.CollectionReceipts, "usage": models.CollectionUsage, "facts": models.CollectionSituationFacts, "signals": models.CollectionRestrictedSignals}

func (m *Mongo) List(ctx context.Context, kind, organizationID string, limit int) ([]bson.M, error) {
	collection, ok := allowed[kind]
	if !ok {
		return nil, fmt.Errorf("unsupported management collection")
	}
	if organizationID == "" {
		return nil, fmt.Errorf("organization_id is required")
	}
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	cursor, err := m.db.Collection(collection).Find(ctx, bson.M{"organization_id": organizationID}, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}, {Key: "received_at", Value: -1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var result []bson.M
	if err := cursor.All(ctx, &result); err != nil {
		return nil, err
	}
	for _, document := range result {
		delete(document, "_id")
		delete(document, "decision_lease_token")
		if kind == "observations" {
			delete(document, "text")
		}
		if kind == "context" {
			delete(document, "payload")
		}
	}
	return result, nil
}
