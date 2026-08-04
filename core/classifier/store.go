package classifier

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/telemetryos/tos-tag/core/database"
	"github.com/telemetryos/tos-tag/models"
	"github.com/telemetryos/tos-tag/types"
)

type DecisionRecord struct {
	ID                    string           `json:"id"`
	OrganizationID        string           `json:"organization_id"`
	ObservationID         string           `json:"observation_id"`
	DecisionRevision      int64            `json:"decision_revision"`
	ContextPackRevisionID types.RevisionID `json:"context_pack_revision_id"`
	OrganizationWatermark int64            `json:"organization_watermark"`
	Result                Result           `json:"result"`
	CreatedAt             time.Time        `json:"created_at"`
}

type DecisionStore interface {
	Record(context.Context, DecisionRecord) (DecisionRecord, bool, error)
	Count(context.Context) (int, error)
	List(context.Context) ([]DecisionRecord, error)
	ListOrganization(context.Context, string) ([]DecisionRecord, error)
}

const decisionListLimit = 500

type MemoryDecisionStore struct {
	mu      sync.RWMutex
	records map[string]DecisionRecord
}

func NewMemoryDecisionStore() *MemoryDecisionStore {
	return &MemoryDecisionStore{records: make(map[string]DecisionRecord)}
}

func (s *MemoryDecisionStore) Record(_ context.Context, record DecisionRecord) (DecisionRecord, bool, error) {
	if record.OrganizationID == "" || record.ObservationID == "" || record.DecisionRevision <= 0 {
		return DecisionRecord{}, false, errors.New("invalid decision record")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := fmt.Sprintf("%s/%s/%d", record.OrganizationID, record.ObservationID, record.DecisionRevision)
	if existing, ok := s.records[key]; ok {
		return existing, false, nil
	}
	if record.ID == "" {
		record.ID = types.NewID("dec")
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	s.records[key] = record
	return record, true, nil
}

func (s *MemoryDecisionStore) List(_ context.Context) ([]DecisionRecord, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]DecisionRecord, 0, len(s.records))
	for _, record := range s.records {
		result = append(result, record)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	if len(result) > decisionListLimit {
		result = result[:decisionListLimit]
	}
	return result, nil
}

func (s *MemoryDecisionStore) Count(_ context.Context) (int, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.records), nil
}

func (s *MemoryDecisionStore) ListOrganization(_ context.Context, organizationID string) ([]DecisionRecord, error) {
	if organizationID == "" {
		return nil, errors.New("organization_id is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]DecisionRecord, 0)
	for _, record := range s.records {
		if record.OrganizationID == organizationID {
			result = append(result, record)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	if len(result) > decisionListLimit {
		result = result[:decisionListLimit]
	}
	return result, nil
}

type MongoDecisionStore struct{ db *database.Database }

func NewMongoDecisionStore(db *database.Database) *MongoDecisionStore {
	return &MongoDecisionStore{db: db}
}

func (s *MongoDecisionStore) Record(ctx context.Context, record DecisionRecord) (DecisionRecord, bool, error) {
	if record.OrganizationID == "" || record.ObservationID == "" || record.DecisionRevision <= 0 {
		return DecisionRecord{}, false, errors.New("invalid decision record")
	}
	if record.ID == "" {
		record.ID = types.NewID("dec")
	}
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	doc := models.ClassificationDecision{PublicID: record.ID, OrganizationID: record.OrganizationID, ObservationID: record.ObservationID, DecisionRevision: record.DecisionRevision, ContextPackRevisionID: string(record.ContextPackRevisionID), OrganizationWatermark: record.OrganizationWatermark, Predicted: record.Result.Predicted, Effective: record.Result.Effective, Shadowed: record.Result.Shadowed, CreatedAt: record.CreatedAt}
	_, err := s.db.Collection(models.CollectionDecisions).InsertOne(ctx, doc)
	if err == nil {
		return record, true, nil
	}
	if !mongo.IsDuplicateKeyError(err) {
		return DecisionRecord{}, false, fmt.Errorf("record decision: %w", err)
	}
	var existing models.ClassificationDecision
	if err := s.db.Collection(models.CollectionDecisions).FindOne(ctx, bson.M{"organization_id": record.OrganizationID, "observation_id": record.ObservationID, "decision_revision": record.DecisionRevision}).Decode(&existing); err != nil {
		return DecisionRecord{}, false, fmt.Errorf("resolve decision: %w", err)
	}
	return decisionFromModel(existing), false, nil
}

func (s *MongoDecisionStore) List(ctx context.Context) ([]DecisionRecord, error) {
	return s.list(ctx, bson.M{}, decisionListLimit, 1)
}

func (s *MongoDecisionStore) Count(ctx context.Context) (int, error) {
	count, err := s.db.Collection(models.CollectionDecisions).CountDocuments(ctx, bson.M{})
	if err != nil {
		return 0, fmt.Errorf("count decisions: %w", err)
	}
	return int(count), nil
}

func (s *MongoDecisionStore) ListOrganization(ctx context.Context, organizationID string) ([]DecisionRecord, error) {
	if organizationID == "" {
		return nil, errors.New("organization_id is required")
	}
	return s.list(ctx, bson.M{"organization_id": organizationID}, decisionListLimit, -1)
}

func (s *MongoDecisionStore) list(ctx context.Context, filter bson.M, limit int64, direction int) ([]DecisionRecord, error) {
	cursor, err := s.db.Collection(models.CollectionDecisions).Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "created_at", Value: direction}}).SetLimit(limit))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var docs []models.ClassificationDecision
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, err
	}
	result := make([]DecisionRecord, len(docs))
	for i, doc := range docs {
		result[i] = decisionFromModel(doc)
	}
	return result, nil
}

func decisionFromModel(doc models.ClassificationDecision) DecisionRecord {
	var predicted, effective types.ClassificationDecision
	if encoded, err := bson.Marshal(doc.Predicted); err == nil {
		_ = bson.Unmarshal(encoded, &predicted)
	}
	if encoded, err := bson.Marshal(doc.Effective); err == nil {
		_ = bson.Unmarshal(encoded, &effective)
	}
	return DecisionRecord{ID: doc.PublicID, OrganizationID: doc.OrganizationID, ObservationID: doc.ObservationID, DecisionRevision: doc.DecisionRevision, ContextPackRevisionID: types.RevisionID(doc.ContextPackRevisionID), OrganizationWatermark: doc.OrganizationWatermark, Result: Result{Predicted: predicted, Effective: effective, Shadowed: doc.Shadowed}, CreatedAt: doc.CreatedAt}
}
