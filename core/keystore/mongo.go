package keystore

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/telemetryos/tos-tag/core/database"
	"github.com/telemetryos/tos-tag/models"
	"github.com/telemetryos/tos-tag/types"
)

type MongoStore struct {
	db   *database.Database
	aead cipher.AEAD
	now  func() time.Time
}

type secretDocument struct {
	Reference  `bson:",inline"`
	Nonce      []byte `bson:"nonce"`
	Ciphertext []byte `bson:"ciphertext"`
}

func NewMongoStore(db *database.Database, masterKey []byte) (*MongoStore, error) {
	if len(masterKey) != 32 {
		return nil, errors.New("keystore master key must be exactly 32 bytes")
	}
	block, err := aes.NewCipher(masterKey)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &MongoStore{db: db, aead: aead, now: time.Now}, nil
}

func (s *MongoStore) Put(ctx context.Context, organizationID, name, purpose, value string) (Reference, error) {
	if organizationID == "" || name == "" || purpose == "" || value == "" {
		return Reference{}, errors.New("organization, name, purpose, and value are required")
	}
	now := s.now().UTC()
	var existing secretDocument
	err := s.db.Collection(models.CollectionSecrets).FindOne(ctx, bson.M{"organization_id": organizationID, "name": name}).Decode(&existing)
	reference := Reference{ID: types.NewID("secret"), OrganizationID: organizationID, Name: name, Purpose: purpose, CreatedAt: now, UpdatedAt: now}
	if err == nil {
		reference.ID = existing.ID
		reference.CreatedAt = existing.CreatedAt
	} else if !errors.Is(err, mongo.ErrNoDocuments) {
		return Reference{}, err
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return Reference{}, err
	}
	aad := []byte(reference.OrganizationID + "\x00" + reference.ID + "\x00" + reference.Name)
	document := secretDocument{Reference: reference, Nonce: nonce, Ciphertext: s.aead.Seal(nil, nonce, []byte(value), aad)}
	_, err = s.db.Collection(models.CollectionSecrets).ReplaceOne(ctx, bson.M{"organization_id": organizationID, "name": name}, document, options.Replace().SetUpsert(true))
	return reference, err
}

func (s *MongoStore) Resolve(ctx context.Context, organizationID, referenceID string) (string, error) {
	var stored secretDocument
	if err := s.db.Collection(models.CollectionSecrets).FindOne(ctx, bson.M{"organization_id": organizationID, "public_id": referenceID}).Decode(&stored); err != nil {
		if errors.Is(err, mongo.ErrNoDocuments) {
			return "", errors.New("secret reference not found")
		}
		return "", err
	}
	aad := []byte(stored.OrganizationID + "\x00" + stored.ID + "\x00" + stored.Name)
	plaintext, err := s.aead.Open(nil, stored.Nonce, stored.Ciphertext, aad)
	if err != nil {
		return "", fmt.Errorf("decrypt secret: %w", err)
	}
	return string(plaintext), nil
}

func (s *MongoStore) List(ctx context.Context, organizationID string) ([]Reference, error) {
	cursor, err := s.db.Collection(models.CollectionSecrets).Find(ctx, bson.M{"organization_id": organizationID}, options.Find().SetProjection(bson.M{"nonce": 0, "ciphertext": 0}).SetSort(bson.D{{Key: "name", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var references []Reference
	if err := cursor.All(ctx, &references); err != nil {
		return nil, err
	}
	return references, nil
}

var _ Repository = (*MongoStore)(nil)
