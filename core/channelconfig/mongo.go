package channelconfig

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/telemetryos/tos-tag/core/database"
	"github.com/telemetryos/tos-tag/models"
	"github.com/telemetryos/tos-tag/types"
)

type MongoStore struct {
	db  *database.Database
	now func() time.Time
}

type projection struct {
	OrganizationID   string `bson:"organization_id"`
	ChannelID        string `bson:"channel_id"`
	NextRevision     int64  `bson:"next_revision"`
	ActiveRevisionID string `bson:"active_revision_id,omitempty"`
}

func NewMongoStore(db *database.Database) *MongoStore {
	return &MongoStore{db: db, now: time.Now}
}

func (s *MongoStore) DraftDirective(ctx context.Context, organizationID, channelID, prompt, actorID string) (DirectiveRevision, error) {
	if organizationID == "" || channelID == "" || strings.TrimSpace(prompt) == "" || actorID == "" {
		return DirectiveRevision{}, errors.New("directive scope, prompt, and actor are required")
	}
	var state projection
	err := s.db.Collection(models.CollectionDirectives).FindOneAndUpdate(ctx,
		bson.M{"organization_id": organizationID, "channel_id": channelID},
		bson.M{"$inc": bson.M{"next_revision": 1}, "$setOnInsert": bson.M{"organization_id": organizationID, "channel_id": channelID}},
		options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After),
	).Decode(&state)
	if err != nil {
		return DirectiveRevision{}, err
	}
	revision := DirectiveRevision{ID: types.NewID("dirrev"), OrganizationID: organizationID, ChannelID: channelID, Revision: state.NextRevision, Prompt: prompt, CreatedBy: actorID, CreatedAt: s.now().UTC()}
	if _, err := s.db.Collection(models.CollectionDirectiveRevisions).InsertOne(ctx, revision); err != nil {
		return DirectiveRevision{}, err
	}
	return revision, nil
}

func (s *MongoStore) ActivateDirective(ctx context.Context, organizationID, channelID, revisionID string) (DirectiveRevision, error) {
	var revision DirectiveRevision
	if err := s.db.Collection(models.CollectionDirectiveRevisions).FindOne(ctx, bson.M{"organization_id": organizationID, "channel_id": channelID, "public_id": revisionID}).Decode(&revision); err != nil {
		return DirectiveRevision{}, normalizeNotFound(err, "directive revision")
	}
	result, err := s.db.Collection(models.CollectionDirectives).UpdateOne(ctx,
		bson.M{"organization_id": organizationID, "channel_id": channelID},
		bson.M{"$set": bson.M{"active_revision_id": revisionID, "activated_at": s.now().UTC()}},
	)
	if err != nil {
		return DirectiveRevision{}, err
	}
	if result.MatchedCount != 1 {
		return DirectiveRevision{}, errors.New("directive projection not found")
	}
	revision.Active = true
	return revision, nil
}

func (s *MongoStore) ActiveDirective(ctx context.Context, organizationID, channelID string) (DirectiveRevision, error) {
	var state projection
	if err := s.db.Collection(models.CollectionDirectives).FindOne(ctx, bson.M{"organization_id": organizationID, "channel_id": channelID}).Decode(&state); err != nil {
		return DirectiveRevision{}, normalizeNotFound(err, "active directive")
	}
	if state.ActiveRevisionID == "" {
		return DirectiveRevision{}, errors.New("active directive not found")
	}
	var revision DirectiveRevision
	if err := s.db.Collection(models.CollectionDirectiveRevisions).FindOne(ctx, bson.M{"organization_id": organizationID, "channel_id": channelID, "public_id": state.ActiveRevisionID}).Decode(&revision); err != nil {
		return DirectiveRevision{}, normalizeNotFound(err, "active directive")
	}
	revision.Active = true
	return revision, nil
}

func (s *MongoStore) ListDirectives(ctx context.Context, organizationID, channelID string) ([]DirectiveRevision, error) {
	filter := scopeFilter(organizationID, channelID)
	cursor, err := s.db.Collection(models.CollectionDirectiveRevisions).Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "revision", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var revisions []DirectiveRevision
	if err := cursor.All(ctx, &revisions); err != nil {
		return nil, err
	}
	var state projection
	_ = s.db.Collection(models.CollectionDirectives).FindOne(ctx, filter).Decode(&state)
	for index := range revisions {
		revisions[index].Active = revisions[index].ID == state.ActiveRevisionID
	}
	return revisions, nil
}

func (s *MongoStore) ProposeNote(ctx context.Context, organizationID, channelID, text string, sourceIDs []string, actorID string) (NoteRevision, error) {
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
	var state projection
	err := s.db.Collection(models.CollectionNotes).FindOneAndUpdate(ctx,
		bson.M{"organization_id": organizationID, "channel_id": channelID},
		bson.M{"$inc": bson.M{"next_revision": 1}, "$setOnInsert": bson.M{"organization_id": organizationID, "channel_id": channelID}},
		options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After),
	).Decode(&state)
	if err != nil {
		return NoteRevision{}, err
	}
	note := NoteRevision{ID: types.NewID("noterev"), OrganizationID: organizationID, ChannelID: channelID, Revision: state.NextRevision, Text: text, SourceIDs: sources, State: NotePending, CreatedBy: actorID, CreatedAt: s.now().UTC()}
	if _, err := s.db.Collection(models.CollectionNoteRevisions).InsertOne(ctx, note); err != nil {
		return NoteRevision{}, err
	}
	return note, nil
}

func (s *MongoStore) ReviewNote(ctx context.Context, organizationID, channelID, noteID, reviewerID string, approve bool) (NoteRevision, error) {
	if reviewerID == "" {
		return NoteRevision{}, errors.New("reviewer is required")
	}
	state := NoteRejected
	if approve {
		state = NoteActive
	}
	var note NoteRevision
	err := s.db.Collection(models.CollectionNoteRevisions).FindOneAndUpdate(ctx,
		bson.M{"organization_id": organizationID, "channel_id": channelID, "public_id": noteID, "state": NotePending, "created_by": bson.M{"$ne": reviewerID}},
		bson.M{"$set": bson.M{"state": state, "reviewed_by": reviewerID, "reviewed_at": s.now().UTC()}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&note)
	if err != nil {
		return NoteRevision{}, normalizeNotFound(err, "pending note revision")
	}
	return note, nil
}

func (s *MongoStore) ListNotes(ctx context.Context, organizationID, channelID string) ([]NoteRevision, error) {
	return s.findNotes(ctx, bson.M{"organization_id": organizationID, "channel_id": channelID})
}

func (s *MongoStore) ActiveNotes(ctx context.Context, organizationID, channelID string) ([]NoteRevision, error) {
	return s.findNotes(ctx, bson.M{"organization_id": organizationID, "channel_id": channelID, "state": NoteActive})
}

func (s *MongoStore) findNotes(ctx context.Context, filter bson.M) ([]NoteRevision, error) {
	cursor, err := s.db.Collection(models.CollectionNoteRevisions).Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "revision", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var notes []NoteRevision
	if err := cursor.All(ctx, &notes); err != nil {
		return nil, err
	}
	return notes, nil
}

func scopeFilter(organizationID, channelID string) bson.M {
	return bson.M{"organization_id": organizationID, "channel_id": channelID}
}

func normalizeNotFound(err error, resource string) error {
	if errors.Is(err, mongo.ErrNoDocuments) {
		return fmt.Errorf("%s not found", resource)
	}
	return err
}

var _ Repository = (*MongoStore)(nil)
