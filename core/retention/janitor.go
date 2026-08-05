// Package retention reconciles Mongo TTL lag and source-to-derived fan-out.
package retention

import (
	"context"
	"fmt"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/telemetryos/tos-tag/core/database"
	"github.com/telemetryos/tos-tag/models"
)

type Status struct {
	LastStartedAt  time.Time `json:"last_started_at,omitempty"`
	LastFinishedAt time.Time `json:"last_finished_at,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
	Sweeps         uint64    `json:"sweeps"`
	DeletedSources int64     `json:"deleted_sources"`
	DeletedDerived int64     `json:"deleted_derived"`
}

type Janitor struct {
	db       *database.Database
	interval time.Duration
	now      func() time.Time
	mu       sync.RWMutex
	status   Status
	cancel   context.CancelFunc
	done     chan struct{}
}

func New(db *database.Database, interval time.Duration) (*Janitor, error) {
	if db == nil || interval <= 0 {
		return nil, fmt.Errorf("database and positive interval are required")
	}
	return &Janitor{db: db, interval: interval, now: time.Now}, nil
}

func (j *Janitor) Start(parent context.Context) {
	j.mu.Lock()
	if j.cancel != nil {
		j.mu.Unlock()
		return
	}
	ctx, cancel := context.WithCancel(parent)
	j.cancel = cancel
	j.done = make(chan struct{})
	done := j.done
	j.mu.Unlock()
	go func() {
		defer close(done)
		ticker := time.NewTicker(j.interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				_, _ = j.Sweep(ctx)
			}
		}
	}()
}

func (j *Janitor) Stop(ctx context.Context) error {
	j.mu.Lock()
	cancel, done := j.cancel, j.done
	j.cancel = nil
	j.mu.Unlock()
	if cancel == nil {
		return nil
	}
	cancel()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (j *Janitor) Status() Status { j.mu.RLock(); defer j.mu.RUnlock(); return j.status }

func (j *Janitor) Sweep(ctx context.Context) (Status, error) {
	now := j.now().UTC()
	j.mu.Lock()
	j.status.LastStartedAt = now
	j.mu.Unlock()
	deletedDerived, deletedSources, err := j.sweep(ctx, now)
	j.mu.Lock()
	defer j.mu.Unlock()
	j.status.Sweeps++
	j.status.LastFinishedAt = j.now().UTC()
	j.status.DeletedDerived += deletedDerived
	j.status.DeletedSources += deletedSources
	if err != nil {
		j.status.LastError = err.Error()
	} else {
		j.status.LastError = ""
	}
	return j.status, err
}

func (j *Janitor) sweep(ctx context.Context, now time.Time) (int64, int64, error) {
	cursor, err := j.db.Collection(models.CollectionDerivations).Find(ctx, bson.M{"expires_at": bson.M{"$lte": now}}, options.Find().SetSort(bson.D{{Key: "expires_at", Value: 1}}).SetLimit(1000))
	if err != nil {
		return 0, 0, err
	}
	defer cursor.Close(ctx)
	allowed := map[string]bool{models.CollectionContextPacks: true, models.CollectionSituationFacts: true, models.CollectionRestrictedSignals: true, models.CollectionSummaries: true}
	var links []models.SourceDerivation
	if err := cursor.All(ctx, &links); err != nil {
		return 0, 0, err
	}
	derivedIDs := make(map[string][]string)
	linkIDs := make([]bson.ObjectID, 0, len(links))
	for _, link := range links {
		linkIDs = append(linkIDs, link.ID)
		if !allowed[link.DerivedCollection] {
			continue
		}
		derivedIDs[link.DerivedCollection] = append(derivedIDs[link.DerivedCollection], link.DerivedID)
	}
	var derived int64
	for collection, ids := range derivedIDs {
		result, err := j.db.Collection(collection).DeleteMany(ctx, bson.M{"public_id": bson.M{"$in": ids}})
		if err != nil {
			return derived, 0, err
		}
		derived += result.DeletedCount
	}
	if len(linkIDs) > 0 {
		if _, err := j.db.Collection(models.CollectionDerivations).DeleteMany(ctx, bson.M{"_id": bson.M{"$in": linkIDs}}); err != nil {
			return derived, 0, err
		}
	}
	collections := []string{models.CollectionObservations, models.CollectionContextPacks, models.CollectionSituationFacts, models.CollectionRestrictedSignals, models.CollectionSummaries, models.CollectionJobs, models.CollectionDeliveries}
	var sources int64
	for _, collection := range collections {
		result, err := j.db.Collection(collection).DeleteMany(ctx, bson.M{"expires_at": bson.M{"$lte": now}})
		if err != nil {
			return derived, sources, err
		}
		sources += result.DeletedCount
	}
	return derived, sources, nil
}

func ClampExpiry(requested time.Time, sources ...time.Time) (time.Time, error) {
	if requested.IsZero() || len(sources) == 0 {
		return time.Time{}, fmt.Errorf("requested and source expiry are required")
	}
	result := requested
	for _, source := range sources {
		if source.IsZero() {
			return time.Time{}, fmt.Errorf("source expiry is required")
		}
		if source.Before(result) {
			result = source
		}
	}
	return result.UTC(), nil
}
