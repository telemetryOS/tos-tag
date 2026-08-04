// Package channelconfig owns revisioned channel directives and source-linked
// human-reviewed notes. Note content is data, never instruction authority.
package channelconfig

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/telemetryos/tos-tag/types"
)

type DirectiveRevision struct {
	ID             string    `json:"id" bson:"public_id"`
	OrganizationID string    `json:"organization_id" bson:"organization_id"`
	ChannelID      string    `json:"channel_id" bson:"channel_id"`
	Revision       int64     `json:"revision" bson:"revision"`
	Prompt         string    `json:"prompt" bson:"prompt"`
	CreatedBy      string    `json:"created_by" bson:"created_by"`
	CreatedAt      time.Time `json:"created_at" bson:"created_at"`
	SourceID       string    `json:"source_id,omitempty" bson:"source_id,omitempty"`
	Active         bool      `json:"active" bson:"-"`
}

type NoteState string

const (
	NotePending  NoteState = "pending_review"
	NoteActive   NoteState = "active"
	NoteRejected NoteState = "rejected"
)

type NoteRevision struct {
	ID             string    `json:"id" bson:"public_id"`
	OrganizationID string    `json:"organization_id" bson:"organization_id"`
	ChannelID      string    `json:"channel_id" bson:"channel_id"`
	Revision       int64     `json:"revision" bson:"revision"`
	Text           string    `json:"text" bson:"text"`
	SourceIDs      []string  `json:"source_ids" bson:"source_ids"`
	State          NoteState `json:"state" bson:"state"`
	CreatedBy      string    `json:"created_by" bson:"created_by"`
	ReviewedBy     string    `json:"reviewed_by,omitempty" bson:"reviewed_by,omitempty"`
	CreatedAt      time.Time `json:"created_at" bson:"created_at"`
}

type Repository interface {
	DraftDirective(context.Context, string, string, string, string) (DirectiveRevision, error)
	PublishDirective(context.Context, string, string, string, string, string) (DirectiveRevision, error)
	ActivateDirective(context.Context, string, string, string) (DirectiveRevision, error)
	ActiveDirective(context.Context, string, string) (DirectiveRevision, error)
	ListDirectives(context.Context, string, string) ([]DirectiveRevision, error)
	ListNotes(context.Context, string, string) ([]NoteRevision, error)
	ActiveNotes(context.Context, string, string) ([]NoteRevision, error)
	ProposeNote(context.Context, string, string, string, []string, string) (NoteRevision, error)
	ReviewNote(context.Context, string, string, string, string, bool) (NoteRevision, error)
}

type Store struct {
	mu         sync.Mutex
	directives map[string][]DirectiveRevision
	notes      map[string][]NoteRevision
}

func NewStore() *Store {
	return &Store{directives: make(map[string][]DirectiveRevision), notes: make(map[string][]NoteRevision)}
}

func (s *Store) DraftDirective(_ context.Context, organizationID, channelID, prompt, actorID string) (DirectiveRevision, error) {
	prompt, err := validateDirective(organizationID, channelID, prompt, actorID)
	if err != nil {
		return DirectiveRevision{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := scopeKey(organizationID, channelID)
	revision := DirectiveRevision{ID: types.NewID("dirrev"), OrganizationID: organizationID, ChannelID: channelID, Revision: int64(len(s.directives[key]) + 1), Prompt: prompt, CreatedBy: actorID, CreatedAt: time.Now().UTC()}
	s.directives[key] = append(s.directives[key], revision)
	return revision, nil
}

func (s *Store) PublishDirective(_ context.Context, organizationID, channelID, prompt, actorID, sourceID string) (DirectiveRevision, error) {
	prompt, err := validateDirective(organizationID, channelID, prompt, actorID)
	if err != nil {
		return DirectiveRevision{}, err
	}
	if strings.TrimSpace(sourceID) == "" {
		return DirectiveRevision{}, errors.New("directive source ID is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := scopeKey(organizationID, channelID)
	for index := range s.directives[key] {
		if s.directives[key][index].SourceID != sourceID {
			continue
		}
		for other := range s.directives[key] {
			s.directives[key][other].Active = other == index
		}
		return s.directives[key][index], nil
	}
	for index := range s.directives[key] {
		s.directives[key][index].Active = false
	}
	revision := DirectiveRevision{ID: types.NewID("dirrev"), OrganizationID: organizationID, ChannelID: channelID, Revision: int64(len(s.directives[key]) + 1), Prompt: prompt, CreatedBy: actorID, CreatedAt: time.Now().UTC(), SourceID: sourceID, Active: true}
	s.directives[key] = append(s.directives[key], revision)
	return revision, nil
}

func (s *Store) ActivateDirective(_ context.Context, organizationID, channelID, revisionID string) (DirectiveRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := scopeKey(organizationID, channelID)
	revisions := s.directives[key]
	found := -1
	for index := range revisions {
		if revisions[index].ID == revisionID {
			found = index
			break
		}
	}
	if found < 0 {
		return DirectiveRevision{}, errors.New("directive revision not found")
	}
	for index := range revisions {
		revisions[index].Active = index == found
	}
	s.directives[key] = revisions
	return revisions[found], nil
}

func (s *Store) ActiveDirective(_ context.Context, organizationID, channelID string) (DirectiveRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, revision := range s.directives[scopeKey(organizationID, channelID)] {
		if revision.Active {
			return revision, nil
		}
	}
	return DirectiveRevision{}, errors.New("active directive not found")
}

func (s *Store) ListDirectives(_ context.Context, organizationID, channelID string) ([]DirectiveRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if channelID == "" {
		var result []DirectiveRevision
		prefix := organizationID + "/"
		for key, revisions := range s.directives {
			if strings.HasPrefix(key, prefix) {
				result = append(result, revisions...)
			}
		}
		sort.Slice(result, func(i, j int) bool {
			if result[i].ChannelID == result[j].ChannelID {
				return result[i].Revision < result[j].Revision
			}
			return result[i].ChannelID < result[j].ChannelID
		})
		return result, nil
	}
	return append([]DirectiveRevision(nil), s.directives[scopeKey(organizationID, channelID)]...), nil
}
func (s *Store) ListNotes(_ context.Context, organizationID, channelID string) ([]NoteRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if channelID == "" {
		var result []NoteRevision
		prefix := organizationID + "/"
		for key, revisions := range s.notes {
			if strings.HasPrefix(key, prefix) {
				result = append(result, revisions...)
			}
		}
		sort.Slice(result, func(i, j int) bool {
			if result[i].ChannelID == result[j].ChannelID {
				return result[i].Revision < result[j].Revision
			}
			return result[i].ChannelID < result[j].ChannelID
		})
		return result, nil
	}
	return append([]NoteRevision(nil), s.notes[scopeKey(organizationID, channelID)]...), nil
}
func (s *Store) ActiveNotes(_ context.Context, organizationID, channelID string) ([]NoteRevision, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var result []NoteRevision
	for _, note := range s.notes[scopeKey(organizationID, channelID)] {
		if note.State == NoteActive {
			result = append(result, note)
		}
	}
	return result, nil
}

func (s *Store) ProposeNote(_ context.Context, organizationID, channelID, text string, sourceIDs []string, actorID string) (NoteRevision, error) {
	if organizationID == "" || channelID == "" || strings.TrimSpace(text) == "" || len(sourceIDs) == 0 || actorID == "" {
		return NoteRevision{}, errors.New("note scope, text, source IDs, and actor are required")
	}
	sources := append([]string(nil), sourceIDs...)
	sort.Strings(sources)
	for index := 1; index < len(sources); index++ {
		if sources[index] == sources[index-1] {
			return NoteRevision{}, errors.New("duplicate note source")
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := scopeKey(organizationID, channelID)
	note := NoteRevision{ID: types.NewID("noterev"), OrganizationID: organizationID, ChannelID: channelID, Revision: int64(len(s.notes[key]) + 1), Text: text, SourceIDs: sources, State: NotePending, CreatedBy: actorID, CreatedAt: time.Now().UTC()}
	s.notes[key] = append(s.notes[key], note)
	return note, nil
}

func (s *Store) ReviewNote(_ context.Context, organizationID, channelID, noteID, reviewerID string, approve bool) (NoteRevision, error) {
	if reviewerID == "" {
		return NoteRevision{}, errors.New("reviewer is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := scopeKey(organizationID, channelID)
	for index := range s.notes[key] {
		if s.notes[key][index].ID != noteID {
			continue
		}
		if s.notes[key][index].State != NotePending {
			return NoteRevision{}, errors.New("note is no longer pending")
		}
		if s.notes[key][index].CreatedBy == reviewerID {
			return NoteRevision{}, errors.New("independent reviewer required")
		}
		s.notes[key][index].ReviewedBy = reviewerID
		if approve {
			s.notes[key][index].State = NoteActive
		} else {
			s.notes[key][index].State = NoteRejected
		}
		return s.notes[key][index], nil
	}
	return NoteRevision{}, errors.New("note revision not found")
}

func DelimitedNoteData(note NoteRevision) string {
	return fmt.Sprintf("<channel-note id=%q state=%q>\n%s\n</channel-note>", note.ID, note.State, note.Text)
}

func scopeKey(organizationID, channelID string) string { return organizationID + "/" + channelID }

func validateDirective(organizationID, channelID, prompt, actorID string) (string, error) {
	prompt = strings.TrimSpace(prompt)
	if organizationID == "" || channelID == "" || prompt == "" || actorID == "" {
		return "", errors.New("directive scope, prompt, and actor are required")
	}
	if len([]rune(prompt)) > 3000 {
		return "", errors.New("directive prompt exceeds 3000 characters")
	}
	return prompt, nil
}
