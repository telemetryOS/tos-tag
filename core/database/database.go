// Package database owns MongoDB connection lifecycle and index contracts.
package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/RobertWHurst/blackbox"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.opentelemetry.io/contrib/instrumentation/go.mongodb.org/mongo-driver/v2/mongo/otelmongo"

	"github.com/telemetryos/tos-tag/core/config"
	"github.com/telemetryos/tos-tag/models"
)

type IndexSpec struct {
	Collection string
	Model      mongo.IndexModel
}

type Database struct {
	cfg    *config.Config
	logger *blackbox.Logger
	client *mongo.Client
}

func New(cfg *config.Config, logger *blackbox.Logger) *Database {
	return &Database{cfg: cfg, logger: logger}
}

func (d *Database) Connect(ctx context.Context) error {
	connectCtx, cancel := context.WithTimeout(ctx, d.cfg.Mongo.Timeout)
	defer cancel()
	opts := options.Client().ApplyURI(d.cfg.Mongo.URI).SetAppName(d.cfg.Telemetry.ServiceName).SetServerSelectionTimeout(d.cfg.Mongo.Timeout)
	if d.cfg.Telemetry.OtelEnabled {
		opts.SetMonitor(otelmongo.NewMonitor(otelmongo.WithCommandAttributeDisabled(true)))
	}
	client, err := mongo.Connect(opts)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	if err := client.Ping(connectCtx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return fmt.Errorf("ping: %w", err)
	}
	d.client = client
	if err := d.EnsureIndexes(ctx); err != nil {
		_ = d.Disconnect(context.Background())
		return fmt.Errorf("ensure indexes: %w", err)
	}
	return nil
}

func (d *Database) Disconnect(ctx context.Context) error {
	if d.client == nil {
		return nil
	}
	err := d.client.Disconnect(ctx)
	d.client = nil
	return err
}

func (d *Database) Ping(ctx context.Context) error {
	if d.client == nil {
		return errors.New("mongo client not connected")
	}
	pingCtx, cancel := context.WithTimeout(ctx, d.cfg.Mongo.Timeout)
	defer cancel()
	return d.client.Ping(pingCtx, nil)
}

func (d *Database) Client() *mongo.Client { return d.client }

func (d *Database) DB() *mongo.Database {
	if d.client == nil {
		return nil
	}
	return d.client.Database(d.cfg.Mongo.Database)
}

func (d *Database) Collection(name string) *mongo.Collection {
	return d.DB().Collection(name)
}

func (d *Database) EnsureIndexes(ctx context.Context) error {
	indexCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	for _, spec := range RequiredIndexes() {
		if _, err := d.Collection(spec.Collection).Indexes().CreateOne(indexCtx, spec.Model); err != nil && !isIndexOptionsConflict(err) {
			return fmt.Errorf("%s: %w", spec.Collection, err)
		}
	}
	if d.logger != nil {
		d.logger.Debugf("indexes ensured on %s", d.cfg.Mongo.Database)
	}
	return nil
}

func RequiredIndexes() []IndexSpec {
	unique := func(name string) *options.IndexOptionsBuilder { return options.Index().SetName(name).SetUnique(true) }
	named := func(name string) *options.IndexOptionsBuilder { return options.Index().SetName(name) }
	partialUnique := func(name string, filter bson.M) *options.IndexOptionsBuilder {
		return options.Index().SetName(name).SetUnique(true).SetPartialFilterExpression(filter)
	}
	ttl := func(name string) *options.IndexOptionsBuilder {
		return options.Index().SetName(name).SetExpireAfterSeconds(0)
	}
	return []IndexSpec{
		{models.CollectionOrganizations, mongo.IndexModel{Keys: bson.D{{Key: "public_id", Value: 1}}, Options: unique("organization_public_unique")}},
		{models.CollectionWorkspaces, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "team_id", Value: 1}}, Options: unique("workspace_team_unique")}},
		{models.CollectionChannels, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "team_id", Value: 1}, {Key: "channel_id", Value: 1}}, Options: unique("channel_scope_unique")}},
		{models.CollectionSlackContextSync, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "team_id", Value: 1}, {Key: "channel_id", Value: 1}}, Options: unique("slack_context_sync_scope_unique")}},
		{models.CollectionObservations, mongo.IndexModel{Keys: bson.D{{Key: "team_id", Value: 1}, {Key: "event_id", Value: 1}}, Options: unique("team_event_unique")}},
		{models.CollectionObservations, mongo.IndexModel{Keys: bson.D{{Key: "public_id", Value: 1}}, Options: unique("observation_public_unique")}},
		{models.CollectionObservations, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "channel_id", Value: 1}, {Key: "received_seq", Value: 1}}, Options: unique("channel_receive_unique")}},
		{models.CollectionObservations, mongo.IndexModel{Keys: bson.D{{Key: "decision_state", Value: 1}, {Key: "received_at", Value: 1}, {Key: "organization_received_seq", Value: 1}, {Key: "decision_lease_expires_at", Value: 1}, {Key: "expires_at", Value: 1}}, Options: named("decision_global_claim")}},
		{models.CollectionObservations, mongo.IndexModel{Keys: bson.D{{Key: "expires_at", Value: 1}}, Options: ttl("observation_expiry")}},
		{models.CollectionMessages, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "team_id", Value: 1}, {Key: "channel_id", Value: 1}, {Key: "message_ts", Value: 1}}, Options: unique("message_projection_unique")}},
		{models.CollectionMessages, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "channel_id", Value: 1}, {Key: "original_at", Value: -1}}, Options: named("message_recent")}},
		{models.CollectionMessages, mongo.IndexModel{Keys: bson.D{{Key: "text", Value: "text"}}, Options: named("message_text")}},
		{models.CollectionMessages, mongo.IndexModel{Keys: bson.D{{Key: "expires_at", Value: 1}}, Options: ttl("message_expiry")}},
		{models.CollectionChannelCounters, mongo.IndexModel{Keys: bson.D{{Key: "_id", Value: 1}}, Options: named("channel_counter")}},
		{models.CollectionOrganizationCounts, mongo.IndexModel{Keys: bson.D{{Key: "_id", Value: 1}}, Options: named("organization_counter")}},
		{models.CollectionDecisions, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "observation_id", Value: 1}, {Key: "decision_revision", Value: 1}}, Options: unique("decision_revision_unique")}},
		{models.CollectionDecisions, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "created_at", Value: -1}}, Options: named("decision_recent")}},
		{models.CollectionContextPacks, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "target_observation_id", Value: 1}, {Key: "revision", Value: 1}}, Options: unique("context_pack_revision")}},
		{models.CollectionContextPacks, mongo.IndexModel{Keys: bson.D{{Key: "expires_at", Value: 1}}, Options: ttl("context_pack_expiry")}},
		{models.CollectionSessions, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "team_id", Value: 1}, {Key: "channel_id", Value: 1}, {Key: "root_thread_ts", Value: 1}}, Options: unique("thread_session_unique")}},
		{models.CollectionGenerations, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "session_id", Value: 1}, {Key: "generation", Value: 1}}, Options: unique("session_generation_unique")}},
		{models.CollectionJobs, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "idempotency_key", Value: 1}}, Options: unique("job_idempotency_unique")}},
		{models.CollectionJobs, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "created_at", Value: -1}}, Options: named("job_recent")}},
		{models.CollectionJobs, mongo.IndexModel{Keys: bson.D{{Key: "public_id", Value: 1}}, Options: unique("job_public_unique")}},
		{models.CollectionJobs, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "state", Value: 1}, {Key: "available_at", Value: 1}, {Key: "lease.expires_at", Value: 1}}, Options: named("job_claim")}},
		{models.CollectionJobs, mongo.IndexModel{Keys: bson.D{{Key: "state", Value: 1}, {Key: "available_at", Value: 1}, {Key: "expires_at", Value: 1}, {Key: "created_at", Value: 1}}, Options: named("job_global_claim")}},
		{models.CollectionJobs, mongo.IndexModel{Keys: bson.D{{Key: "state", Value: 1}, {Key: "lease.expires_at", Value: 1}}, Options: named("job_lease_recovery")}},
		{models.CollectionJobs, mongo.IndexModel{Keys: bson.D{{Key: "state", Value: 1}, {Key: "final_delivery_enqueued", Value: 1}, {Key: "expires_at", Value: 1}, {Key: "available_at", Value: 1}}, Options: named("job_reconciliation")}},
		{models.CollectionJobs, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "session_id", Value: 1}, {Key: "generation", Value: 1}}, Options: partialUnique("session_generation_writer_unique", bson.M{"writer_active": true})}},
		{models.CollectionJobs, mongo.IndexModel{Keys: bson.D{{Key: "expires_at", Value: 1}}, Options: ttl("job_expiry")}},
		{models.CollectionDeliveries, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "idempotency_key", Value: 1}}, Options: unique("delivery_idempotency_unique")}},
		{models.CollectionDeliveries, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "created_at", Value: -1}}, Options: named("delivery_recent")}},
		{models.CollectionDeliveries, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "status", Value: 1}, {Key: "retry_at", Value: 1}}, Options: named("delivery_claim")}},
		{models.CollectionDeliveries, mongo.IndexModel{Keys: bson.D{{Key: "expires_at", Value: 1}}, Options: ttl("delivery_expiry")}},
		{models.CollectionDerivations, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "source_id", Value: 1}, {Key: "derived_collection", Value: 1}, {Key: "derived_id", Value: 1}}, Options: unique("source_derivation_unique")}},
		{models.CollectionDerivations, mongo.IndexModel{Keys: bson.D{{Key: "expires_at", Value: 1}}, Options: ttl("source_derivation_expiry")}},
		{models.CollectionSituationFacts, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "status", Value: 1}, {Key: "updated_at", Value: -1}}, Options: named("situation_active")}},
		{models.CollectionSituationFacts, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "channel_id", Value: 1}, {Key: "message_ts", Value: 1}, {Key: "kind", Value: 1}}, Options: unique("situation_source_unique")}},
		{models.CollectionSituationFacts, mongo.IndexModel{Keys: bson.D{{Key: "expires_at", Value: 1}}, Options: ttl("situation_expiry")}},
		{models.CollectionRestrictedSignals, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "channel_id", Value: 1}, {Key: "message_ts", Value: 1}, {Key: "kind", Value: 1}}, Options: unique("restricted_source_unique")}},
		{models.CollectionRestrictedSignals, mongo.IndexModel{Keys: bson.D{{Key: "expires_at", Value: 1}}, Options: ttl("restricted_signal_expiry")}},
		{models.CollectionSummaries, mongo.IndexModel{Keys: bson.D{{Key: "expires_at", Value: 1}}, Options: ttl("summary_expiry")}},
		{models.CollectionSummaries, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "scope_key", Value: 1}}, Options: partialUnique("memory_scope_unique", bson.M{"scope_key": bson.M{"$exists": true}})}},
		{models.CollectionSummaries, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "status", Value: 1}, {Key: "restricted", Value: 1}, {Key: "channel_id", Value: 1}, {Key: "updated_at", Value: -1}}, Options: named("memory_recall")}},
		{models.CollectionProjectorWatermarks, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}}, Options: unique("projector_watermark_unique")}},
		{models.CollectionReceipts, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "sequence", Value: 1}}, Options: unique("audit_sequence_unique")}},
		{models.CollectionReceipts, mongo.IndexModel{Keys: bson.D{{Key: "public_id", Value: 1}}, Options: unique("audit_public_unique")}},
		{models.CollectionReceipts, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "idempotency_key", Value: 1}}, Options: partialUnique("audit_idempotency_unique", bson.M{"idempotency_key": bson.M{"$exists": true}})}},
		{models.CollectionAuditHeads, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}}, Options: unique("audit_head_unique")}},
		{models.CollectionModelProfiles, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "id", Value: 1}}, Options: unique("model_profile_unique")}},
		{models.CollectionModelRules, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "id", Value: 1}}, Options: unique("model_rule_unique")}},
		{models.CollectionDirectives, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "channel_id", Value: 1}}, Options: unique("channel_directive_unique")}},
		{models.CollectionDirectiveRevisions, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "channel_id", Value: 1}, {Key: "revision", Value: 1}}, Options: unique("channel_directive_revision_unique")}},
		{models.CollectionDirectiveRevisions, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "public_id", Value: 1}}, Options: unique("channel_directive_public_unique")}},
		{models.CollectionDirectiveRevisions, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "source_id", Value: 1}}, Options: partialUnique("channel_directive_source_unique", bson.M{"source_id": bson.M{"$exists": true}})}},
		{models.CollectionNotes, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "channel_id", Value: 1}}, Options: unique("channel_note_unique")}},
		{models.CollectionNoteRevisions, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "channel_id", Value: 1}, {Key: "revision", Value: 1}}, Options: unique("channel_note_revision_unique")}},
		{models.CollectionNoteRevisions, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "public_id", Value: 1}}, Options: unique("channel_note_public_unique")}},
		{models.CollectionNoteRevisions, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "channel_id", Value: 1}, {Key: "state", Value: 1}}, Options: named("channel_note_active")}},
		{models.CollectionUsage, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "created_at", Value: -1}}, Options: named("usage_recent")}},
		{models.CollectionSecrets, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "name", Value: 1}}, Options: unique("secret_name_unique")}},
		{models.CollectionSecrets, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "public_id", Value: 1}}, Options: unique("secret_public_unique")}},
		{models.CollectionAdmissionStates, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "team_id", Value: 1}, {Key: "channel_id", Value: 1}}, Options: unique("admission_scope_unique")}},
		{models.CollectionAdmissionReservations, mongo.IndexModel{Keys: bson.D{{Key: "state_id", Value: 1}, {Key: "completed", Value: 1}, {Key: "expires_at", Value: 1}}, Options: named("admission_expiry_reconcile")}},
		{models.CollectionAdmissionReservations, mongo.IndexModel{Keys: bson.D{{Key: "cleanup_at", Value: 1}}, Options: ttl("admission_reservation_cleanup")}},
		{models.CollectionClassifierFloodBuckets, mongo.IndexModel{Keys: bson.D{{Key: "expires_at", Value: 1}}, Options: ttl("classifier_flood_bucket_cleanup")}},
		{models.CollectionApprovals, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "public_id", Value: 1}}, Options: unique("approval_public_unique")}},
		{models.CollectionApprovals, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "expires_at", Value: 1}}, Options: named("approval_pending")}},
		{models.CollectionApprovals, mongo.IndexModel{Keys: bson.D{{Key: "cleanup_at", Value: 1}}, Options: ttl("approval_cleanup")}},
		{models.CollectionRoutines, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "public_id", Value: 1}}, Options: unique("routine_public_unique")}},
		{models.CollectionRoutines, mongo.IndexModel{Keys: bson.D{{Key: "enabled", Value: 1}, {Key: "next_run", Value: 1}}, Options: named("routine_due")}},
		{models.CollectionEventSubscriptions, mongo.IndexModel{Keys: bson.D{{Key: "organization_id", Value: 1}, {Key: "public_id", Value: 1}}, Options: unique("event_subscription_public_unique")}},
		{models.CollectionEventSubscriptions, mongo.IndexModel{Keys: bson.D{{Key: "enabled", Value: 1}, {Key: "next_run", Value: 1}}, Options: named("event_subscription_due")}},
	}
}

func isIndexOptionsConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "IndexOptionsConflict")
}
