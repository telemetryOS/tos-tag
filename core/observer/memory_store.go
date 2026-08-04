package observer

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/telemetryos/tos-tag/models"
	"github.com/telemetryos/tos-tag/types"
)

type MemoryStore struct {
	mu sync.RWMutex

	now              func() time.Time
	messageRetention time.Duration
	observations     map[string]models.Observation
	byPublic         map[string]string
	messages         map[string]models.ChannelMessage
	channelSeq       map[string]int64
	organizationSeq  map[string]int64
}

func NewMemoryStore(messageRetention time.Duration, now func() time.Time) *MemoryStore {
	if now == nil {
		now = time.Now
	}
	return &MemoryStore{
		now:              now,
		messageRetention: messageRetention,
		observations:     make(map[string]models.Observation),
		byPublic:         make(map[string]string),
		messages:         make(map[string]models.ChannelMessage),
		channelSeq:       make(map[string]int64),
		organizationSeq:  make(map[string]int64),
	}
}

func (s *MemoryStore) Accept(_ context.Context, envelope types.SlackEnvelope) (Acceptance, error) {
	return s.accept(envelope, "pending", "pending")
}

// Import persists user-authorized Slack history as resolved context. Imported
// history is retrieval material, never a pending ambient trigger.
func (s *MemoryStore) Import(_ context.Context, envelope types.SlackEnvelope) (Acceptance, error) {
	return s.accept(envelope, "authorized", "resolved")
}

func (s *MemoryStore) accept(envelope types.SlackEnvelope, scopeState, decisionState string) (Acceptance, error) {
	if err := ValidateEnvelope(envelope); err != nil {
		return Acceptance{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	dedupeKey := envelope.TeamID + "/" + envelope.EventID
	if existing, ok := s.observations[dedupeKey]; ok {
		s.applyProjection(envelope, existing)
		return Acceptance{Observation: existing, Duplicate: true}, nil
	}
	now := s.now().UTC()
	s.channelSeq[envelope.OrganizationID+"/"+envelope.TeamID+"/"+envelope.ChannelID]++
	s.organizationSeq[envelope.OrganizationID]++
	at := eventTime(envelope, now)
	observation := models.Observation{
		PublicID:                types.NewID("obs"),
		OrganizationID:          envelope.OrganizationID,
		TeamID:                  envelope.TeamID,
		ChannelID:               envelope.ChannelID,
		EventID:                 envelope.EventID,
		EnvelopeID:              envelope.EnvelopeID,
		ReceivedSeq:             s.channelSeq[envelope.OrganizationID+"/"+envelope.TeamID+"/"+envelope.ChannelID],
		OrganizationReceivedSeq: s.organizationSeq[envelope.OrganizationID],
		SlackEventTime:          at,
		ReceivedAt:              now,
		MessageTS:               envelope.MessageTS,
		RootThreadTS:            envelope.RootThreadTS(),
		UserID:                  envelope.UserID,
		BotID:                   envelope.BotID,
		EventType:               string(envelope.Kind),
		Subtype:                 envelope.Subtype,
		Text:                    envelope.Text,
		MutationTargetTS:        envelope.TargetTS,
		ScopeState:              scopeState,
		DecisionState:           decisionState,
		Restricted:              envelope.Restricted,
		IsMention:               envelope.IsMention,
		OriginTag:               envelope.OriginTag,
		CreatedAt:               now,
		ExpiresAt:               at.Add(s.messageRetention),
		Version:                 1,
	}
	s.observations[dedupeKey] = observation
	s.byPublic[observation.PublicID] = dedupeKey
	s.applyProjection(envelope, observation)
	return Acceptance{Observation: observation}, nil
}

func (s *MemoryStore) ClaimPending(_ context.Context, owner string, lease time.Duration) (models.Observation, error) {
	if owner == "" || lease <= 0 {
		return models.Observation{}, ErrInvalidEnvelope
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	var selectedKey string
	var selected models.Observation
	for key, observation := range s.observations {
		eligible := observation.DecisionState == "pending" || (observation.DecisionState == "processing" && !observation.DecisionLeaseExpiresAt.After(now))
		if !eligible || (!selected.CreatedAt.IsZero() && observation.OrganizationReceivedSeq >= selected.OrganizationReceivedSeq) {
			continue
		}
		selectedKey, selected = key, observation
	}
	if selectedKey == "" {
		return models.Observation{}, ErrNoPendingObservation
	}
	selected.DecisionState = "processing"
	selected.DecisionLeaseOwner = owner
	selected.DecisionLeaseToken = types.NewID("obslease")
	selected.DecisionLeaseExpiresAt = now.Add(lease)
	selected.Version++
	s.observations[selectedKey] = selected
	return selected, nil
}

func (s *MemoryStore) CompleteDecision(_ context.Context, publicID, leaseToken, scopeState, decisionState string) error {
	if publicID == "" || leaseToken == "" || scopeState == "" || decisionState == "" {
		return ErrInvalidEnvelope
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.byPublic[publicID]
	if !ok {
		return ErrMessageNotFound
	}
	observation := s.observations[key]
	if observation.DecisionState != "processing" || observation.DecisionLeaseToken != leaseToken || !observation.DecisionLeaseExpiresAt.After(s.now().UTC()) {
		return ErrNoPendingObservation
	}
	observation.ScopeState, observation.DecisionState = scopeState, decisionState
	observation.DecisionLeaseOwner, observation.DecisionLeaseToken = "", ""
	observation.DecisionLeaseExpiresAt = time.Time{}
	observation.Version++
	s.observations[key] = observation
	return nil
}

func (s *MemoryStore) applyProjection(envelope types.SlackEnvelope, observation models.Observation) {
	messageTS := projectionMessageTS(envelope)
	key := messageKey(envelope.OrganizationID, envelope.TeamID, envelope.ChannelID, messageTS)
	now := s.now().UTC()
	eventAt := observation.SlackEventTime.UTC()
	eventRank := projectionEventRank(envelope.Kind)
	if current, ok := s.messages[key]; ok {
		if !projectionIsNewer(current, eventAt, eventRank) {
			return
		}
		current.SourceEventID = envelope.EventID
		current.SourceEventAt = eventAt
		current.SourceEventRank = eventRank
		current.UpdatedAt = now
		current.ProjectionVersion++
		if envelope.Kind == types.SlackEventDelete {
			current.Deleted = true
			current.Text = ""
		} else {
			current.Deleted = false
			current.Text = envelope.Text
		}
		if envelope.UserID != "" {
			current.AuthorID = envelope.UserID
		}
		if envelope.BotID != "" {
			current.BotID = envelope.BotID
		}
		if envelope.Subtype != "" {
			current.Subtype = envelope.Subtype
		}
		current.Restricted = envelope.Restricted
		s.messages[key] = current
		return
	}
	originalAt := observation.SlackEventTime
	text := envelope.Text
	if envelope.Kind == types.SlackEventDelete {
		text = ""
	}
	s.messages[key] = models.ChannelMessage{
		OrganizationID:    envelope.OrganizationID,
		TeamID:            envelope.TeamID,
		ChannelID:         envelope.ChannelID,
		MessageTS:         messageTS,
		RootThreadTS:      envelope.RootThreadTS(),
		AuthorID:          envelope.UserID,
		BotID:             envelope.BotID,
		Subtype:           envelope.Subtype,
		Text:              text,
		Deleted:           envelope.Kind == types.SlackEventDelete,
		Restricted:        envelope.Restricted,
		SourceEventID:     envelope.EventID,
		SourceEventAt:     eventAt,
		SourceEventRank:   eventRank,
		ProjectionVersion: 1,
		OriginalAt:        originalAt,
		UpdatedAt:         now,
		ExpiresAt:         originalAt.Add(s.messageRetention),
	}
}

func (s *MemoryStore) SetRestricted(_ context.Context, publicID string, restricted bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.byPublic[publicID]
	if !ok {
		return ErrMessageNotFound
	}
	observation := s.observations[key]
	observation.Restricted = restricted
	observation.Version++
	s.observations[key] = observation
	messageTS := observation.MessageTS
	if observation.MutationTargetTS != "" {
		messageTS = observation.MutationTargetTS
	}
	projectionKey := messageKey(observation.OrganizationID, observation.TeamID, observation.ChannelID, messageTS)
	if message, exists := s.messages[projectionKey]; exists {
		message.Restricted = restricted
		message.UpdatedAt = s.now().UTC()
		message.ProjectionVersion++
		s.messages[projectionKey] = message
	}
	return nil
}

func (s *MemoryStore) Recent(_ context.Context, organizationID string, channelIDs []string, since time.Time, limit int) ([]models.ChannelMessage, error) {
	if limit <= 0 {
		return nil, nil
	}
	allowed := make(map[string]struct{}, len(channelIDs))
	for _, id := range channelIDs {
		allowed[id] = struct{}{}
	}
	now := s.now().UTC()
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]models.ChannelMessage, 0, limit)
	for _, message := range s.messages {
		if message.OrganizationID != organizationID || message.Deleted || !message.ExpiresAt.After(now) || message.OriginalAt.Before(since) {
			continue
		}
		if _, ok := allowed[message.ChannelID]; !ok {
			continue
		}
		result = append(result, message)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].OriginalAt.Equal(result[j].OriginalAt) {
			return result[i].MessageTS < result[j].MessageTS
		}
		return result[i].OriginalAt.Before(result[j].OriginalAt)
	})
	if len(result) > limit {
		result = result[len(result)-limit:]
	}
	return result, nil
}

func (s *MemoryStore) CurrentMessage(_ context.Context, organizationID, teamID, channelID, messageTS string) (models.ChannelMessage, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	message, ok := s.messages[messageKey(organizationID, teamID, channelID, messageTS)]
	if !ok || !message.ExpiresAt.After(s.now().UTC()) {
		return models.ChannelMessage{}, ErrMessageNotFound
	}
	return message, nil
}

func (s *MemoryStore) Channels(_ context.Context, organizationID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	set := make(map[string]struct{})
	for _, message := range s.messages {
		if message.OrganizationID == organizationID {
			set[message.ChannelID] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for channel := range set {
		result = append(result, channel)
	}
	sort.Strings(result)
	return result, nil
}

func (s *MemoryStore) MarkOutput(_ context.Context, observationID, jobID, deliveryID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, ok := s.byPublic[observationID]
	if !ok {
		return false, ErrMessageNotFound
	}
	observation := s.observations[key]
	if observation.OutputProduced {
		return false, nil
	}
	observation.OutputProduced = true
	observation.OutputJobID = jobID
	observation.OutputDeliveryID = deliveryID
	observation.Version++
	s.observations[key] = observation
	return true, nil
}

func (s *MemoryStore) LateCandidates(_ context.Context, organizationID string, since, before time.Time, limit int) ([]models.Observation, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var result []models.Observation
	for _, observation := range s.observations {
		if observation.OrganizationID != organizationID || observation.DecisionState != "decided" || observation.OutputProduced || observation.SlackEventTime.Before(since) || !observation.SlackEventTime.Before(before) || observation.EventType != string(types.SlackEventMessage) || !isStatusQuestion(observation.Text) {
			continue
		}
		result = append(result, observation)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].SlackEventTime.After(result[j].SlackEventTime) })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}
func isStatusQuestion(text string) bool {
	value := strings.ToLower(text)
	return strings.Contains(value, "system down") || strings.Contains(value, "is it down") || strings.Contains(value, "outage?")
}

var ErrMessageNotFound = &messageNotFoundError{}

type messageNotFoundError struct{}

func (*messageNotFoundError) Error() string { return "message not found" }

func messageKey(parts ...string) string { return strings.Join(parts, "/") }
