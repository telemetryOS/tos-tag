// Package audit creates canonical, tamper-evident, content-redacted receipts.
package audit

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/telemetryos/tos-tag/types"
)

type Receipt struct {
	ID                string         `json:"id" bson:"public_id"`
	OrganizationID    string         `json:"organization_id" bson:"organization_id"`
	Sequence          int64          `json:"sequence" bson:"sequence"`
	Type              string         `json:"type" bson:"type"`
	ActorID           string         `json:"actor_id,omitempty" bson:"actor_id,omitempty"`
	ResourceID        string         `json:"resource_id,omitempty" bson:"resource_id,omitempty"`
	Metadata          map[string]any `json:"metadata,omitempty" bson:"metadata,omitempty"`
	RetentionEpoch    string         `json:"retention_epoch" bson:"retention_epoch"`
	IdempotencyKey    string         `json:"idempotency_key,omitempty" bson:"idempotency_key,omitempty"`
	ContentCommitment string         `json:"content_commitment,omitempty" bson:"content_commitment,omitempty"`
	PreviousHash      string         `json:"previous_hash,omitempty" bson:"previous_hash,omitempty"`
	Hash              string         `json:"hash" bson:"hash"`
	CreatedAt         time.Time      `json:"created_at" bson:"created_at"`
}

type AppendRequest struct {
	OrganizationID string
	Type           string
	ActorID        string
	ResourceID     string
	Metadata       map[string]any
	RetentionEpoch string
	IdempotencyKey string
	Content        []byte
}

type Appender interface {
	Append(context.Context, AppendRequest) (Receipt, error)
}

type MemoryAppender struct{ chain *Chain }

func NewMemoryAppender(hmacKey []byte) (*MemoryAppender, error) {
	chain, err := New(hmacKey)
	if err != nil {
		return nil, err
	}
	return &MemoryAppender{chain: chain}, nil
}

func (m *MemoryAppender) Append(_ context.Context, request AppendRequest) (Receipt, error) {
	return m.chain.Append(request)
}

type Chain struct {
	mu       sync.Mutex
	hmacKey  []byte
	receipts map[string][]Receipt
	byKey    map[string]Receipt
}

func New(hmacKey []byte) (*Chain, error) {
	if len(hmacKey) < 32 {
		return nil, errors.New("audit HMAC key must be at least 32 bytes")
	}
	return &Chain{hmacKey: append([]byte(nil), hmacKey...), receipts: make(map[string][]Receipt), byKey: make(map[string]Receipt)}, nil
}

func (c *Chain) Append(request AppendRequest) (Receipt, error) {
	if request.OrganizationID == "" || request.Type == "" || request.RetentionEpoch == "" {
		return Receipt{}, errors.New("organization, type, and retention epoch are required")
	}
	if err := validateMetadata(request.Metadata); err != nil {
		return Receipt{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if request.IdempotencyKey != "" {
		if existing, ok := c.byKey[request.OrganizationID+"/"+request.IdempotencyKey]; ok {
			return existing, nil
		}
	}
	current := c.receipts[request.OrganizationID]
	receipt := Receipt{
		ID: types.NewID("rcpt"), OrganizationID: request.OrganizationID, Sequence: int64(len(current) + 1),
		Type: request.Type, ActorID: request.ActorID, ResourceID: request.ResourceID, Metadata: cloneMap(request.Metadata),
		RetentionEpoch: request.RetentionEpoch, IdempotencyKey: request.IdempotencyKey, CreatedAt: time.Now().UTC().Truncate(time.Millisecond),
	}
	if len(current) > 0 {
		receipt.PreviousHash = current[len(current)-1].Hash
	}
	if len(request.Content) > 0 {
		receipt.ContentCommitment = c.commit(request.RetentionEpoch, request.Content)
	}
	hash, err := hashReceipt(receipt)
	if err != nil {
		return Receipt{}, err
	}
	receipt.Hash = hash
	c.receipts[request.OrganizationID] = append(current, receipt)
	if request.IdempotencyKey != "" {
		c.byKey[request.OrganizationID+"/"+request.IdempotencyKey] = receipt
	}
	return receipt, nil
}

func (c *Chain) List(organizationID string) []Receipt {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]Receipt(nil), c.receipts[organizationID]...)
}

func (c *Chain) Verify(organizationID string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var previous string
	for index, receipt := range c.receipts[organizationID] {
		if receipt.Sequence != int64(index+1) || receipt.PreviousHash != previous {
			return fmt.Errorf("audit chain discontinuity at sequence %d", receipt.Sequence)
		}
		expected, err := hashReceipt(receipt)
		if err != nil {
			return err
		}
		if !hmac.Equal([]byte(receipt.Hash), []byte(expected)) {
			return fmt.Errorf("audit receipt tampered at sequence %d", receipt.Sequence)
		}
		previous = receipt.Hash
	}
	return nil
}

func (c *Chain) commit(epoch string, content []byte) string {
	mac := hmac.New(sha256.New, c.hmacKey)
	_, _ = mac.Write([]byte(epoch))
	_, _ = mac.Write([]byte{0})
	_, _ = mac.Write(content)
	return "hmac-sha256:" + hex.EncodeToString(mac.Sum(nil))
}

func hashReceipt(receipt Receipt) (string, error) {
	receipt.Hash = ""
	canonical, err := json.Marshal(receipt)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return map[string]any{"redaction_error": true}
	}
	var output map[string]any
	if json.Unmarshal(encoded, &output) != nil {
		return map[string]any{"redaction_error": true}
	}
	return output
}

func validateMetadata(metadata map[string]any) error {
	for key, value := range metadata {
		lower := strings.ToLower(key)
		for _, forbidden := range []string{"text", "content", "prompt", "body", "message", "secret", "token", "password", "credential"} {
			if strings.Contains(lower, forbidden) {
				return fmt.Errorf("audit metadata field %q is not content-free", key)
			}
		}
		switch typed := value.(type) {
		case nil, bool, float64, float32, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		case string:
			if len(typed) > 256 {
				return fmt.Errorf("audit metadata value %q is too long", key)
			}
		case []string:
			if len(typed) > 32 {
				return fmt.Errorf("audit metadata list %q is too long", key)
			}
			for _, item := range typed {
				if len(item) > 256 {
					return fmt.Errorf("audit metadata item %q is too long", key)
				}
			}
		default:
			return fmt.Errorf("audit metadata field %q has unsupported type", key)
		}
	}
	return nil
}
