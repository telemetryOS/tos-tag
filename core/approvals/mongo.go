package approvals

import (
	"context"
	"errors"
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

func (s *MongoStore) RequestContext(ctx context.Context, action Action, requesterID string, ttl time.Duration) (Approval, error) {
	if requesterID == "" || action.OrganizationID == "" || ttl <= 0 {
		return Approval{}, errors.New("requester, organization, and positive TTL are required")
	}
	hash, err := HashAction(action)
	if err != nil {
		return Approval{}, err
	}
	now := s.now().UTC()
	approval := Approval{ID: types.NewID("approval"), OrganizationID: action.OrganizationID, RequesterID: requesterID, ActionHash: hash, Action: action, ExpiresAt: now.Add(ttl), CleanupAt: now.Add(ttl + 24*time.Hour)}
	_, err = s.db.Collection(models.CollectionApprovals).InsertOne(ctx, approvalDocument(approval))
	return approval, err
}

func (s *MongoStore) ApproveContext(ctx context.Context, organizationID, id, approverID string) (Approval, error) {
	if organizationID == "" || id == "" || approverID == "" {
		return Approval{}, errors.New("organization, approval, and approver are required")
	}
	now := s.now().UTC()
	after := options.After
	var doc approvalDoc
	err := s.db.Collection(models.CollectionApprovals).FindOneAndUpdate(ctx, bson.M{"organization_id": organizationID, "public_id": id, "requester_id": bson.M{"$ne": approverID}, "approver_id": "", "approved_at": time.Time{}, "consumed_at": time.Time{}, "expires_at": bson.M{"$gt": now}}, bson.M{"$set": bson.M{"approver_id": approverID, "approved_at": now}}, options.FindOneAndUpdate().SetReturnDocument(after)).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Approval{}, errors.New("approval is not approvable")
	}
	return doc.approval(), err
}

func (s *MongoStore) ConsumeContext(ctx context.Context, organizationID, id string, action Action) (Approval, error) {
	hash, err := HashAction(action)
	if err != nil || action.OrganizationID != organizationID {
		return Approval{}, errors.New("approval does not authorize these action bytes")
	}
	now := s.now().UTC()
	after := options.After
	var doc approvalDoc
	err = s.db.Collection(models.CollectionApprovals).FindOneAndUpdate(ctx, bson.M{"organization_id": organizationID, "public_id": id, "action_hash": hash, "approved_at": bson.M{"$ne": time.Time{}}, "consumed_at": time.Time{}, "expires_at": bson.M{"$gt": now}}, bson.M{"$set": bson.M{"consumed_at": now}}, options.FindOneAndUpdate().SetReturnDocument(after)).Decode(&doc)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return Approval{}, errors.New("approval does not authorize these action bytes")
	}
	return doc.approval(), err
}

func (s *MongoStore) List(ctx context.Context, organizationID string) ([]Approval, error) {
	if organizationID == "" {
		return nil, errors.New("organization is required")
	}
	cursor, err := s.db.Collection(models.CollectionApprovals).Find(ctx, bson.M{"organization_id": organizationID}, options.Find().SetSort(bson.D{{Key: "expires_at", Value: -1}}))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var docs []approvalDoc
	if err := cursor.All(ctx, &docs); err != nil {
		return nil, err
	}
	result := make([]Approval, len(docs))
	for i := range docs {
		result[i] = docs[i].approval()
	}
	return result, nil
}

type approvalDoc struct {
	ID             string    `bson:"public_id"`
	OrganizationID string    `bson:"organization_id"`
	RequesterID    string    `bson:"requester_id"`
	ApproverID     string    `bson:"approver_id"`
	ActionHash     string    `bson:"action_hash"`
	Action         Action    `bson:"action"`
	ExpiresAt      time.Time `bson:"expires_at"`
	ApprovedAt     time.Time `bson:"approved_at"`
	ConsumedAt     time.Time `bson:"consumed_at"`
	CleanupAt      time.Time `bson:"cleanup_at"`
}

func approvalDocument(value Approval) approvalDoc {
	return approvalDoc{value.ID, value.OrganizationID, value.RequesterID, value.ApproverID, value.ActionHash, value.Action, value.ExpiresAt, value.ApprovedAt, value.ConsumedAt, value.CleanupAt}
}
func (d approvalDoc) approval() Approval {
	return Approval{ID: d.ID, OrganizationID: d.OrganizationID, RequesterID: d.RequesterID, ApproverID: d.ApproverID, ActionHash: d.ActionHash, Action: d.Action, ExpiresAt: d.ExpiresAt, ApprovedAt: d.ApprovedAt, ConsumedAt: d.ConsumedAt, CleanupAt: d.CleanupAt}
}

var _ Repository = (*MongoStore)(nil)
