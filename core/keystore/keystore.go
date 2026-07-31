// Package keystore encrypts tool/provider secret values and exposes only
// opaque references to callers that enumerate configuration.
package keystore

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"

	"github.com/telemetryos/tos-tag/types"
)

type Reference struct {
	ID             string    `json:"id" bson:"public_id"`
	OrganizationID string    `json:"organization_id" bson:"organization_id"`
	Name           string    `json:"name" bson:"name"`
	Purpose        string    `json:"purpose" bson:"purpose"`
	CreatedAt      time.Time `json:"created_at" bson:"created_at"`
	UpdatedAt      time.Time `json:"updated_at" bson:"updated_at"`
}

type encryptedValue struct {
	Reference
	Nonce      []byte
	Ciphertext []byte
}

type Store struct {
	mu     sync.RWMutex
	aead   cipher.AEAD
	values map[string]encryptedValue
	byName map[string]string
}

type Repository interface {
	Put(context.Context, string, string, string, string) (Reference, error)
	Resolve(context.Context, string, string) (string, error)
	List(context.Context, string) ([]Reference, error)
}

func New(masterKey []byte) (*Store, error) {
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
	return &Store{aead: aead, values: make(map[string]encryptedValue), byName: make(map[string]string)}, nil
}

func (s *Store) Put(_ context.Context, organizationID, name, purpose, value string) (Reference, error) {
	if organizationID == "" || name == "" || purpose == "" || value == "" {
		return Reference{}, errors.New("organization, name, purpose, and value are required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	key := organizationID + "/" + name
	reference := Reference{ID: types.NewID("secret"), OrganizationID: organizationID, Name: name, Purpose: purpose, CreatedAt: now, UpdatedAt: now}
	if id := s.byName[key]; id != "" {
		reference = s.values[id].Reference
		reference.UpdatedAt = now
	}
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return Reference{}, err
	}
	aad := []byte(reference.OrganizationID + "\x00" + reference.ID + "\x00" + reference.Name)
	ciphertext := s.aead.Seal(nil, nonce, []byte(value), aad)
	s.values[reference.ID] = encryptedValue{Reference: reference, Nonce: nonce, Ciphertext: ciphertext}
	s.byName[key] = reference.ID
	return reference, nil
}

func (s *Store) Resolve(_ context.Context, organizationID, referenceID string) (string, error) {
	s.mu.RLock()
	stored, ok := s.values[referenceID]
	s.mu.RUnlock()
	if !ok || stored.OrganizationID != organizationID {
		return "", errors.New("secret reference not found")
	}
	aad := []byte(stored.OrganizationID + "\x00" + stored.ID + "\x00" + stored.Name)
	plaintext, err := s.aead.Open(nil, stored.Nonce, stored.Ciphertext, aad)
	if err != nil {
		return "", fmt.Errorf("decrypt secret: %w", err)
	}
	return string(plaintext), nil
}

func (s *Store) List(_ context.Context, organizationID string) ([]Reference, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Reference, 0)
	for _, stored := range s.values {
		if stored.OrganizationID == organizationID {
			result = append(result, stored.Reference)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

var _ Repository = (*Store)(nil)
