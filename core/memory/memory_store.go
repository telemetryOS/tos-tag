package memory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/telemetryos/tos-tag/types"
)

// MemoryStore is the deterministic implementation used by tests. MongoStore
// remains the production authority behind the same interface.
type MemoryStore struct {
	mu      sync.Mutex
	now     func() time.Time
	byID    map[string]Record
	byScope map[string]string
}

func NewMemoryStore(now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{now: now, byID: make(map[string]Record), byScope: make(map[string]string)}
}

func (s *MemoryStore) List(_ context.Context, organizationID string, limit int) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var records []Record
	for _, record := range s.byID {
		if record.OrganizationID == organizationID {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].UpdatedAt.After(records[j].UpdatedAt) })
	if limit > 0 && len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}

func (s *MemoryStore) Recall(_ context.Context, organizationID, channelID, rootThreadTS string, now time.Time, limit int) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var records []Record
	for _, record := range s.byID {
		if record.OrganizationID != organizationID || record.Status != StatusActive || (record.Origin != "operator" && len(record.Facts) == 0) || (!record.Pinned && !record.ExpiresAt.After(now)) || (record.Restricted && record.ChannelID != channelID) || (record.Scope == ScopeThread && (record.ChannelID != channelID || record.RootThreadTS != rootThreadTS)) {
			continue
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].UpdatedAt.After(records[j].UpdatedAt) })
	if limit > 0 && len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}

func (s *MemoryStore) FindScope(_ context.Context, organizationID, scopeKey string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.byScope[organizationID+"/"+scopeKey]
	record, ok := s.byID[id]
	if !ok {
		return Record{}, ErrNotFound
	}
	return record, nil
}

func (s *MemoryStore) PutGenerated(_ context.Context, record Record) (Record, bool, error) {
	text, err := validateText(record.Text)
	if err != nil {
		return Record{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := record.OrganizationID + "/" + record.ScopeKey
	if id := s.byScope[key]; id != "" {
		existing := s.byID[id]
		if existing.SourceHash == record.SourceHash || (existing.Status == StatusActive && (existing.Pinned || existing.Origin == "operator")) {
			return existing, false, nil
		}
		record.ID, record.CreatedAt, record.Revision = existing.ID, existing.CreatedAt, existing.Revision+1
	}
	now := s.now().UTC()
	if record.ID == "" {
		record.ID, record.CreatedAt, record.Revision = types.NewID("mem"), now, 1
	}
	record.Text, record.UpdatedAt, record.Origin = text, now, "model"
	s.byID[record.ID], s.byScope[key] = record, record.ID
	return record, true, nil
}

func (s *MemoryStore) Correct(_ context.Context, organizationID, recordID, text, actorID string) (Record, error) {
	text, err := validateText(text)
	if err != nil {
		return Record{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.byID[recordID]
	if !ok || record.OrganizationID != organizationID || actorID == "" {
		return Record{}, ErrNotFound
	}
	record.Text, record.Origin, record.Status, record.Pinned, record.ExpiresAt, record.UpdatedAt = text, "operator", StatusActive, true, time.Time{}, s.now().UTC()
	record.Revision++
	s.byID[recordID] = record
	return record, nil
}

func (s *MemoryStore) SetPinned(_ context.Context, organizationID, recordID string, pinned bool, actorID string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.byID[recordID]
	if !ok || record.OrganizationID != organizationID || actorID == "" {
		return Record{}, ErrNotFound
	}
	record.Pinned, record.UpdatedAt = pinned, s.now().UTC()
	if pinned {
		record.ExpiresAt = time.Time{}
	} else {
		record.ExpiresAt = record.NaturalExpiresAt
	}
	record.Revision++
	s.byID[recordID] = record
	return record, nil
}

func (s *MemoryStore) Forget(_ context.Context, organizationID, recordID, actorID string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.byID[recordID]
	if !ok || record.OrganizationID != organizationID || actorID == "" {
		return Record{}, ErrNotFound
	}
	record.Text, record.Facts, record.SourceIDs, record.Model, record.ReasoningEffort = "", nil, nil, "", ""
	record.Status, record.Origin, record.Pinned, record.ExpiresAt, record.NaturalExpiresAt, record.UpdatedAt = StatusForgotten, "operator", false, time.Time{}, time.Time{}, s.now().UTC()
	record.Revision++
	s.byID[recordID] = record
	return record, nil
}
