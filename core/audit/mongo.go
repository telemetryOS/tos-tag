package audit

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/telemetryos/tos-tag/core/database"
	"github.com/telemetryos/tos-tag/models"
	"github.com/telemetryos/tos-tag/types"
)

type mongoHead struct {
	OrganizationID string    `bson:"organization_id"`
	Sequence       int64     `bson:"sequence"`
	Hash           string    `bson:"hash"`
	Version        int64     `bson:"version"`
	UpdatedAt      time.Time `bson:"updated_at"`
}
type MongoChain struct {
	db     *database.Database
	signer *Chain
	now    func() time.Time
}

func NewMongoChain(db *database.Database, hmacKey []byte) (*MongoChain, error) {
	signer, err := New(hmacKey)
	if err != nil {
		return nil, err
	}
	return &MongoChain{db: db, signer: signer, now: time.Now}, nil
}

func (c *MongoChain) Append(ctx context.Context, request AppendRequest) (Receipt, error) {
	if request.OrganizationID == "" || request.Type == "" || request.RetentionEpoch == "" {
		return Receipt{}, errors.New("organization, type, and retention epoch are required")
	}
	if err := validateMetadata(request.Metadata); err != nil {
		return Receipt{}, err
	}
	if request.IdempotencyKey != "" {
		var existing Receipt
		err := c.db.Collection(models.CollectionReceipts).FindOne(ctx, bson.M{"organization_id": request.OrganizationID, "idempotency_key": request.IdempotencyKey}).Decode(&existing)
		if err == nil {
			return existing, nil
		}
		if !errors.Is(err, mongo.ErrNoDocuments) {
			return Receipt{}, err
		}
	}
	for attempt := 0; attempt < 32; attempt++ {
		head, err := c.head(ctx, request.OrganizationID)
		if err != nil {
			return Receipt{}, err
		}
		if advanced, err := c.recoverNext(ctx, head); err != nil {
			return Receipt{}, err
		} else if advanced {
			continue
		}
		receipt := Receipt{ID: types.NewID("rcpt"), OrganizationID: request.OrganizationID, Sequence: head.Sequence + 1, Type: request.Type, ActorID: request.ActorID, ResourceID: request.ResourceID, Metadata: cloneMap(request.Metadata), RetentionEpoch: request.RetentionEpoch, IdempotencyKey: request.IdempotencyKey, PreviousHash: head.Hash, CreatedAt: c.now().UTC().Truncate(time.Millisecond)}
		if len(request.Content) > 0 {
			receipt.ContentCommitment = c.signer.commit(request.RetentionEpoch, request.Content)
		}
		receipt.Hash, err = hashReceipt(receipt)
		if err != nil {
			return Receipt{}, err
		}
		_, err = c.db.Collection(models.CollectionReceipts).InsertOne(ctx, receipt)
		if mongo.IsDuplicateKeyError(err) {
			if request.IdempotencyKey != "" {
				var existing Receipt
				if findErr := c.db.Collection(models.CollectionReceipts).FindOne(ctx, bson.M{"organization_id": request.OrganizationID, "idempotency_key": request.IdempotencyKey}).Decode(&existing); findErr == nil {
					return existing, nil
				}
			}
			continue
		}
		if err != nil {
			return Receipt{}, err
		}
		filter := bson.M{"organization_id": request.OrganizationID, "sequence": head.Sequence, "version": head.Version}
		update := bson.M{"$set": bson.M{"hash": receipt.Hash, "updated_at": c.now().UTC()}, "$inc": bson.M{"sequence": 1, "version": 1}, "$setOnInsert": bson.M{"organization_id": request.OrganizationID}}
		result, err := c.db.Collection(models.CollectionAuditHeads).UpdateOne(ctx, filter, update, options.UpdateOne().SetUpsert(head.Version == 0))
		if err == nil && result.MatchedCount+result.UpsertedCount == 1 {
			return receipt, nil
		}
		current, headErr := c.head(ctx, request.OrganizationID)
		if headErr == nil && current.Sequence == receipt.Sequence && current.Hash == receipt.Hash {
			return receipt, nil
		}
		// The receipt insert is durable even when another writer advances the
		// head before this writer observes its CAS result. Return this exact
		// attempt instead of creating a second key-less receipt on retry;
		// recoverNext owns adoption of any temporarily orphaned receipt.
		var persisted Receipt
		if findErr := c.db.Collection(models.CollectionReceipts).FindOne(ctx, bson.M{"organization_id": request.OrganizationID, "public_id": receipt.ID}).Decode(&persisted); findErr == nil {
			return persisted, nil
		} else if !errors.Is(findErr, mongo.ErrNoDocuments) {
			return Receipt{}, findErr
		}
		// Never delete a valid orphan here. Another writer may have adopted this
		// exact receipt between the head read above and a compensating delete.
		// recoverNext is the single owner of orphan adoption on the next retry.
		if err != nil && !mongo.IsDuplicateKeyError(err) {
			return Receipt{}, err
		}
	}
	return Receipt{}, fmt.Errorf("audit CAS contention exceeded retry limit")
}

func (c *MongoChain) head(ctx context.Context, org string) (mongoHead, error) {
	var head mongoHead
	err := c.db.Collection(models.CollectionAuditHeads).FindOne(ctx, bson.M{"organization_id": org}).Decode(&head)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return mongoHead{OrganizationID: org}, nil
	}
	return head, err
}
func (c *MongoChain) recoverNext(ctx context.Context, head mongoHead) (bool, error) {
	var receipt Receipt
	err := c.db.Collection(models.CollectionReceipts).FindOne(ctx, bson.M{"organization_id": head.OrganizationID, "sequence": head.Sequence + 1}).Decode(&receipt)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	expected, err := hashReceipt(receipt)
	if err != nil || receipt.PreviousHash != head.Hash || expected != receipt.Hash {
		return false, fmt.Errorf("invalid orphan audit receipt at sequence %d", receipt.Sequence)
	}
	result, err := c.db.Collection(models.CollectionAuditHeads).UpdateOne(ctx, bson.M{"organization_id": head.OrganizationID, "sequence": head.Sequence, "version": head.Version}, bson.M{"$set": bson.M{"hash": receipt.Hash, "updated_at": c.now().UTC()}, "$inc": bson.M{"sequence": 1, "version": 1}, "$setOnInsert": bson.M{"organization_id": head.OrganizationID}}, options.UpdateOne().SetUpsert(head.Version == 0))
	if mongo.IsDuplicateKeyError(err) {
		return false, nil
	}
	return err == nil && result.MatchedCount+result.UpsertedCount == 1, err
}
func (c *MongoChain) List(ctx context.Context, org string) ([]Receipt, error) {
	cursor, err := c.db.Collection(models.CollectionReceipts).Find(ctx, bson.M{"organization_id": org}, options.Find().SetSort(bson.D{{Key: "sequence", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var receipts []Receipt
	err = cursor.All(ctx, &receipts)
	return receipts, err
}
func (c *MongoChain) Verify(ctx context.Context, org string) error {
	receipts, err := c.List(ctx, org)
	if err != nil {
		return err
	}
	sort.Slice(receipts, func(i, j int) bool { return receipts[i].Sequence < receipts[j].Sequence })
	previous := ""
	for index, receipt := range receipts {
		if receipt.Sequence != int64(index+1) || receipt.PreviousHash != previous {
			return fmt.Errorf("audit chain discontinuity at sequence %d", receipt.Sequence)
		}
		expected, err := hashReceipt(receipt)
		if err != nil || expected != receipt.Hash {
			return fmt.Errorf("audit receipt tampered at sequence %d", receipt.Sequence)
		}
		previous = receipt.Hash
	}
	head, err := c.head(ctx, org)
	if err != nil {
		return err
	}
	if head.Sequence != int64(len(receipts)) || head.Hash != previous {
		return fmt.Errorf("audit head mismatch")
	}
	return nil
}

func (c *MongoChain) VerifyContentCommitment(epoch string, content []byte, commitment string) bool {
	return c.signer.VerifyContentCommitment(epoch, content, commitment)
}
