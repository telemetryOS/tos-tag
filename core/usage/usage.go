// Package usage records content-free model, worker, tool, and delivery usage.
package usage

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/mongo/options"

	"github.com/telemetryos/tos-tag/core/database"
	"github.com/telemetryos/tos-tag/models"
	"github.com/telemetryos/tos-tag/types"
)

type Event struct {
	ID             string    `json:"id" bson:"public_id"`
	OrganizationID string    `json:"organization_id" bson:"organization_id"`
	JobID          string    `json:"job_id,omitempty" bson:"job_id,omitempty"`
	Category       string    `json:"category" bson:"category"`
	ProviderID     string    `json:"provider_id,omitempty" bson:"provider_id,omitempty"`
	ModelID        string    `json:"model_id,omitempty" bson:"model_id,omitempty"`
	ProfileID      string    `json:"profile_id,omitempty" bson:"profile_id,omitempty"`
	InputTokens    int64     `json:"input_tokens,omitempty" bson:"input_tokens,omitempty"`
	OutputTokens   int64     `json:"output_tokens,omitempty" bson:"output_tokens,omitempty"`
	Calls          int64     `json:"calls" bson:"calls"`
	DurationMS     int64     `json:"duration_ms,omitempty" bson:"duration_ms,omitempty"`
	CreatedAt      time.Time `json:"created_at" bson:"created_at"`
}
type Recorder interface {
	Record(context.Context, Event) error
	List(context.Context, string, int) ([]Event, error)
}

func validate(event Event) error {
	if event.OrganizationID == "" || event.Category == "" || event.Calls < 0 || event.InputTokens < 0 || event.OutputTokens < 0 || event.DurationMS < 0 {
		return fmt.Errorf("invalid content-free usage event")
	}
	return nil
}

type Memory struct {
	mu     sync.Mutex
	events []Event
}

func NewMemory() *Memory { return &Memory{} }
func (m *Memory) Record(_ context.Context, event Event) error {
	if err := validate(event); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if event.ID == "" {
		event.ID = types.NewID("usage")
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	m.events = append(m.events, event)
	return nil
}
func (m *Memory) List(_ context.Context, organizationID string, limit int) ([]Event, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []Event
	for index := len(m.events) - 1; index >= 0 && len(result) < limit; index-- {
		if m.events[index].OrganizationID == organizationID {
			result = append(result, m.events[index])
		}
	}
	return result, nil
}

type Mongo struct{ db *database.Database }

func NewMongo(db *database.Database) *Mongo { return &Mongo{db: db} }
func (m *Mongo) Record(ctx context.Context, event Event) error {
	if err := validate(event); err != nil {
		return err
	}
	if event.ID == "" {
		event.ID = types.NewID("usage")
	}
	if event.CreatedAt.IsZero() {
		event.CreatedAt = time.Now().UTC()
	}
	_, err := m.db.Collection(models.CollectionUsage).InsertOne(ctx, event)
	return err
}
func (m *Mongo) List(ctx context.Context, organizationID string, limit int) ([]Event, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	cursor, err := m.db.Collection(models.CollectionUsage).Find(ctx, bson.M{"organization_id": organizationID}, options.Find().SetSort(bson.D{{Key: "created_at", Value: -1}}).SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}
	defer cursor.Close(ctx)
	var events []Event
	err = cursor.All(ctx, &events)
	return events, err
}
