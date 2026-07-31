// Package approvals binds a human decision to immutable action bytes.
package approvals

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/telemetryos/tos-tag/types"
)

type Action struct {
	OrganizationID string         `json:"organization_id"`
	ToolID         string         `json:"tool_id"`
	ToolVersion    string         `json:"tool_version"`
	OperationID    string         `json:"operation_id"`
	Arguments      map[string]any `json:"arguments"`
	Destination    string         `json:"destination"`
	Risk           string         `json:"risk"`
}

type Approval struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id"`
	RequesterID    string    `json:"requester_id"`
	ApproverID     string    `json:"approver_id,omitempty"`
	ActionHash     string    `json:"action_hash"`
	Action         Action    `json:"action"`
	ExpiresAt      time.Time `json:"expires_at"`
	ApprovedAt     time.Time `json:"approved_at,omitempty"`
	ConsumedAt     time.Time `json:"consumed_at,omitempty"`
	CleanupAt      time.Time `json:"-"`
}

type Repository interface {
	RequestContext(context.Context, Action, string, time.Duration) (Approval, error)
	ApproveContext(context.Context, string, string, string) (Approval, error)
	ConsumeContext(context.Context, string, string, Action) (Approval, error)
	List(context.Context, string) ([]Approval, error)
}

type Store struct {
	mu        sync.Mutex
	approvals map[string]Approval
}

func NewStore() *Store { return &Store{approvals: make(map[string]Approval)} }

func (s *Store) Request(action Action, requesterID string, ttl time.Duration) (Approval, error) {
	if requesterID == "" || action.OrganizationID == "" || ttl <= 0 {
		return Approval{}, errors.New("requester, organization, and positive TTL are required")
	}
	hash, err := HashAction(action)
	if err != nil {
		return Approval{}, err
	}
	now := time.Now().UTC()
	approval := Approval{ID: types.NewID("approval"), OrganizationID: action.OrganizationID, RequesterID: requesterID, ActionHash: hash, Action: action, ExpiresAt: now.Add(ttl), CleanupAt: now.Add(ttl + 24*time.Hour)}
	s.mu.Lock()
	s.approvals[approval.ID] = approval
	s.mu.Unlock()
	return approval, nil
}

func (s *Store) RequestContext(_ context.Context, action Action, requesterID string, ttl time.Duration) (Approval, error) {
	return s.Request(action, requesterID, ttl)
}

func (s *Store) Approve(id, approverID string) (Approval, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	approval, ok := s.approvals[id]
	if !ok {
		return Approval{}, errors.New("approval not found")
	}
	if approverID == "" || approverID == approval.RequesterID {
		return Approval{}, errors.New("independent approver required")
	}
	if !approval.ExpiresAt.After(time.Now().UTC()) || !approval.ApprovedAt.IsZero() || !approval.ConsumedAt.IsZero() {
		return Approval{}, errors.New("approval is no longer approvable")
	}
	approval.ApproverID, approval.ApprovedAt = approverID, time.Now().UTC()
	s.approvals[id] = approval
	return approval, nil
}

func (s *Store) ApproveContext(_ context.Context, organizationID, id, approverID string) (Approval, error) {
	approval, err := s.Approve(id, approverID)
	if err != nil || approval.OrganizationID != organizationID {
		return Approval{}, errors.New("approval not found")
	}
	return approval, nil
}

func (s *Store) Consume(id string, action Action) (Approval, error) {
	hash, err := HashAction(action)
	if err != nil {
		return Approval{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	approval, ok := s.approvals[id]
	if !ok || approval.ApprovedAt.IsZero() || !approval.ConsumedAt.IsZero() || !approval.ExpiresAt.After(time.Now().UTC()) || approval.ActionHash != hash || approval.OrganizationID != action.OrganizationID {
		return Approval{}, errors.New("approval does not authorize these action bytes")
	}
	approval.ConsumedAt = time.Now().UTC()
	s.approvals[id] = approval
	return approval, nil
}

func (s *Store) ConsumeContext(_ context.Context, organizationID, id string, action Action) (Approval, error) {
	if action.OrganizationID != organizationID {
		return Approval{}, errors.New("approval does not authorize this organization")
	}
	return s.Consume(id, action)
}

func (s *Store) List(_ context.Context, organizationID string) ([]Approval, error) {
	if organizationID == "" {
		return nil, errors.New("organization is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]Approval, 0)
	for _, approval := range s.approvals {
		if approval.OrganizationID == organizationID {
			result = append(result, approval)
		}
	}
	return result, nil
}

var _ Repository = (*Store)(nil)

func HashAction(action Action) (string, error) {
	if action.OrganizationID == "" || action.ToolID == "" || action.ToolVersion == "" || action.OperationID == "" || action.Destination == "" || action.Risk == "" {
		return "", errors.New("action is incomplete")
	}
	canonical, err := json.Marshal(action)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(canonical)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
