package integration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/RobertWHurst/blackbox"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/telemetryos/tos-tag/core/admission"
	"github.com/telemetryos/tos-tag/core/approvals"
	"github.com/telemetryos/tos-tag/core/audit"
	"github.com/telemetryos/tos-tag/core/channelconfig"
	"github.com/telemetryos/tos-tag/core/config"
	"github.com/telemetryos/tos-tag/core/database"
	"github.com/telemetryos/tos-tag/core/intelligence"
	"github.com/telemetryos/tos-tag/core/jobs"
	"github.com/telemetryos/tos-tag/core/keystore"
	"github.com/telemetryos/tos-tag/core/modelrouter"
	"github.com/telemetryos/tos-tag/core/observer"
	"github.com/telemetryos/tos-tag/core/orgconfig"
	"github.com/telemetryos/tos-tag/core/retention"
	"github.com/telemetryos/tos-tag/core/routines"
	"github.com/telemetryos/tos-tag/models"
	"github.com/telemetryos/tos-tag/types"
)

func mongoDatabase(t *testing.T) (context.Context, *database.Database) {
	t.Helper()
	if os.Getenv("TOS_TAG_INTEGRATION_MONGO") != "1" {
		t.Skip("set TOS_TAG_INTEGRATION_MONGO=1")
	}
	cfg := config.DefaultConfiguration
	cfg.Mongo.Database = fmt.Sprintf("tos_tag_integration_%d", time.Now().UnixNano())
	db := database.New(&cfg, blackbox.New())
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	if err := db.Connect(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = db.DB().Drop(cleanupCtx)
		_ = db.Disconnect(cleanupCtx)
	})
	return ctx, db
}

func TestMongoChannelDirectiveAndNotePersistence(t *testing.T) {
	ctx, db := mongoDatabase(t)
	first := channelconfig.NewMongoStore(db)
	directive, err := first.DraftDirective(ctx, "org", "support", "Answer incident questions concisely.", "admin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.ActivateDirective(ctx, "org", "support", directive.ID); err != nil {
		t.Fatal(err)
	}
	alerts, err := first.PublishDirective(ctx, "org", "alerts", "Investigate every alert.", "admin", "admin-alerts-1")
	if err != nil {
		t.Fatal(err)
	}
	note, err := first.ProposeNote(ctx, "org", "support", "The status page is canonical.", []string{"support/1.0"}, "agent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.ReviewNote(ctx, "org", "support", note.ID, "reviewer", true); err != nil {
		t.Fatal(err)
	}

	// A new store instance proves these settings are not process-local state.
	second := channelconfig.NewMongoStore(db)
	activeDirective, err := second.ActiveDirective(ctx, "org", "support")
	if err != nil || activeDirective.ID != directive.ID {
		t.Fatalf("directive=%#v err=%v", activeDirective, err)
	}
	allDirectives, err := second.ListDirectives(ctx, "org", "")
	if err != nil || len(allDirectives) != 2 || !allDirectives[0].Active || !allDirectives[1].Active {
		t.Fatalf("organization directives=%#v alerts=%#v err=%v", allDirectives, alerts, err)
	}
	activeNotes, err := second.ActiveNotes(ctx, "org", "support")
	if err != nil || len(activeNotes) != 1 || activeNotes[0].ID != note.ID {
		t.Fatalf("notes=%#v err=%v", activeNotes, err)
	}
}

func TestMongoModelRoutePersistence(t *testing.T) {
	ctx, db := mongoDatabase(t)
	store := modelrouter.NewMongoStore(db)
	profile := types.ModelProfile{ID: "product-deep", ProviderID: "openai", ModelID: "gpt-5.6", Variant: "xhigh", RequiredCapabilities: []string{"structured"}, AllowedDataClasses: []string{"internal"}, MaxInputTokens: 200000, MaxOutputTokens: 16000, Enabled: true}
	if err := store.PutProfile(ctx, profile); err != nil {
		t.Fatal(err)
	}
	rule := modelrouter.Rule{ID: "product", OrganizationID: "org", ChannelID: "product", ProfileID: profile.ID, Priority: 100}
	if err := store.PutRule(ctx, rule); err != nil {
		t.Fatal(err)
	}
	profiles, rules, err := modelrouter.NewMongoStore(db).Load(ctx)
	if err != nil || len(profiles) != 1 || len(rules) != 1 || profiles[0].Variant != "xhigh" || rules[0].ChannelID != "product" {
		t.Fatalf("profiles=%#v rules=%#v err=%v", profiles, rules, err)
	}
}

func TestMongoKeystorePersistenceAndWriteOnlyListing(t *testing.T) {
	ctx, db := mongoDatabase(t)
	key := []byte("01234567890123456789012345678901")
	first, err := keystore.NewMongoStore(db, key)
	if err != nil {
		t.Fatal(err)
	}
	reference, err := first.Put(ctx, "org", "LINEAR_API_KEY", "linear helper", "integration-secret")
	if err != nil {
		t.Fatal(err)
	}
	second, _ := keystore.NewMongoStore(db, key)
	resolved, err := second.Resolve(ctx, "org", reference.ID)
	if err != nil || resolved != "integration-secret" {
		t.Fatalf("resolved=%q err=%v", resolved, err)
	}
	references, err := second.List(ctx, "org")
	if err != nil || len(references) != 1 {
		t.Fatalf("references=%#v err=%v", references, err)
	}
	encoded := fmt.Sprintf("%#v", references)
	if strings.Contains(encoded, "integration-secret") {
		t.Fatal("secret leaked through listing")
	}
}

func TestMongoAdmissionSurvivesControllerRestart(t *testing.T) {
	ctx, db := mongoDatabase(t)
	policy := orgconfig.ChannelPolicy{OrganizationID: "org", TeamID: "team", ChannelID: "support", Enrolled: true, ParticipationMode: types.ModeAssist, MaxResponsesPerHour: 2, MaxConcurrentJobs: 1, MembershipRevision: "m1", MembershipRefreshedAt: time.Now().UTC()}
	first := admission.NewMongo(db)
	reservation, err := first.Admit(ctx, policy)
	if err != nil {
		t.Fatal(err)
	}
	second := admission.NewMongo(db)
	if _, err := second.Admit(ctx, policy); !errors.Is(err, admission.ErrConcurrency) {
		t.Fatalf("restart lost concurrency state: %v", err)
	}
	second.Complete(ctx, reservation)
	reservation, err = admission.NewMongo(db).Admit(ctx, policy)
	if err != nil {
		t.Fatal(err)
	}
	second.Complete(ctx, reservation)
	if _, err := admission.NewMongo(db).Admit(ctx, policy); !errors.Is(err, admission.ErrBudget) {
		t.Fatalf("restart lost hourly budget state: %v", err)
	}
}

func TestMongoApprovalIsIndependentExactAndSingleUse(t *testing.T) {
	ctx, db := mongoDatabase(t)
	store := approvals.NewMongoStore(db)
	action := approvals.Action{OrganizationID: "org", ToolID: "linear", ToolVersion: "v1", OperationID: "create", Arguments: map[string]any{"title": "incident"}, Destination: "ENG", Risk: "write"}
	approval, err := store.RequestContext(ctx, action, "requester", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApproveContext(ctx, "org", approval.ID, "requester"); err == nil {
		t.Fatal("requester approved their own action")
	}
	if _, err := store.ApproveContext(ctx, "other-org", approval.ID, "reviewer"); err == nil {
		t.Fatal("cross-tenant approval succeeded")
	}
	if _, err := store.ApproveContext(ctx, "org", approval.ID, "reviewer"); err != nil {
		t.Fatal(err)
	}
	changed := action
	changed.Destination = "OTHER"
	if _, err := store.ConsumeContext(ctx, "org", approval.ID, changed); err == nil {
		t.Fatal("approval accepted changed action bytes")
	}
	if _, err := store.ConsumeContext(ctx, "org", approval.ID, action); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ConsumeContext(ctx, "org", approval.ID, action); err == nil {
		t.Fatal("approval was consumed twice")
	}
}

func TestMongoRoutineAdvanceIsTenantScoped(t *testing.T) {
	ctx, db := mongoDatabase(t)
	store := routines.NewMongoStore(db)
	now := time.Now().UTC().Truncate(time.Millisecond)
	base := routines.Routine{ID: "shared", WorkspaceID: "workspace", ChannelID: "channel", SessionID: "session", Generation: 1, OwnerID: "owner", Input: "brief", Interval: time.Hour, NextRun: now, Enabled: true}
	first := base
	first.OrganizationID = "org-a"
	second := base
	second.OrganizationID = "org-b"
	if _, err := store.PutContext(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutContext(ctx, second); err != nil {
		t.Fatal(err)
	}
	if err := store.AdvanceContext(ctx, "org-a", "shared", now); err != nil {
		t.Fatal(err)
	}
	a, err := store.List(ctx, "org-a")
	if err != nil || len(a) != 1 || !a[0].NextRun.After(now) {
		t.Fatalf("org-a routine was not advanced: routines=%#v err=%v", a, err)
	}
	b, err := store.List(ctx, "org-b")
	if err != nil || len(b) != 1 || !b[0].NextRun.Equal(now) {
		t.Fatalf("org-b routine changed across tenant boundary: routines=%#v err=%v", b, err)
	}
}

func TestMongoIndexesDedupeCountersFencingAndTTL(t *testing.T) {
	ctx, db := mongoDatabase(t)
	names, err := db.Collection(models.CollectionJobs).Indexes().ListSpecifications(ctx)
	if err != nil || len(names) < 3 {
		t.Fatalf("job indexes=%d err=%v", len(names), err)
	}
	store := observer.NewMongoStore(db, 30*24*time.Hour)
	now := time.Now().UTC()
	envelope := types.SlackEnvelope{OrganizationID: "org", TeamID: "team", ChannelID: "channel", EnvelopeID: "env", EventID: "event", MessageTS: "1.0", UserID: "user", Kind: types.SlackEventMessage, Text: "hello", EventTime: now}
	var wg sync.WaitGroup
	results := make(chan observer.Acceptance, 16)
	errs := make(chan error, 16)
	for range 16 {
		wg.Add(1)
		go func() { defer wg.Done(); value, err := store.Accept(ctx, envelope); results <- value; errs <- err }()
	}
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	unique := map[string]bool{}
	for result := range results {
		unique[result.Observation.PublicID] = true
	}
	if len(unique) != 1 {
		t.Fatalf("dedupe produced %d observations", len(unique))
	}
	queue := jobs.NewMongoQueue(db)
	spec := jobs.Spec{OrganizationID: "org", WorkspaceID: "team", ChannelID: "channel", RootThreadTS: "1.0", SessionID: "session", Generation: 1, Kind: "agent", MaxAttempts: 2}
	spec.IdempotencyKey = "one"
	_, _, _ = queue.Enqueue(ctx, spec)
	spec.IdempotencyKey = "two"
	_, _, _ = queue.Enqueue(ctx, spec)
	claimed, err := queue.Claim(ctx, "worker", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Claim(ctx, "other", time.Minute); !errors.Is(err, jobs.ErrNoRunnableJob) {
		t.Fatalf("one-writer fence failed: %v", err)
	}
	if err := queue.Heartbeat(ctx, claimed.ID, "forged", time.Minute); !errors.Is(err, jobs.ErrLeaseLost) {
		t.Fatalf("forged lease accepted: %v", err)
	}
	expired := envelope
	expired.EnvelopeID = "old-env"
	expired.EventID = "old-event"
	expired.MessageTS = "0.5"
	expired.EventTime = now.Add(-31 * 24 * time.Hour)
	if _, err := store.Accept(ctx, expired); err != nil {
		t.Fatal(err)
	}
	recent, err := store.Recent(ctx, "org", []string{"channel"}, now.Add(-60*24*time.Hour), 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range recent {
		if message.MessageTS == "0.5" {
			t.Fatal("expired message returned before TTL cleanup")
		}
	}
}

func TestMongoProjectionRetentionAndConcurrentAuditCAS(t *testing.T) {
	ctx, db := mongoDatabase(t)
	store := observer.NewMongoStore(db, 30*24*time.Hour)
	projector := intelligence.NewMongo(db)
	now := time.Now().UTC()
	envelope := types.SlackEnvelope{OrganizationID: "org", TeamID: "team", ChannelID: "alerts", EnvelopeID: "env", EventID: "event", MessageTS: "1.0", UserID: "user", Kind: types.SlackEventMessage, Text: "Production outage incident: API is down", EventTime: now}
	accepted, err := store.Accept(ctx, envelope)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projector.Project(ctx, accepted.Observation); err != nil {
		t.Fatal(err)
	}
	count, err := db.Collection(models.CollectionSituationFacts).CountDocuments(ctx, bson.M{"organization_id": "org"})
	if err != nil || count != 1 {
		t.Fatalf("facts=%d err=%v", count, err)
	}
	edit := envelope
	edit.EnvelopeID = "edit-env"
	edit.EventID = "edit-event"
	edit.Kind = types.SlackEventEdit
	edit.TargetTS = "1.0"
	edit.Text = "Recovered and operating normally"
	edited, err := store.Accept(ctx, edit)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projector.Project(ctx, edited.Observation); err != nil {
		t.Fatal(err)
	}
	count, _ = db.Collection(models.CollectionSituationFacts).CountDocuments(ctx, bson.M{"organization_id": "org"})
	if count != 0 {
		t.Fatalf("stale fact survived edit: %d", count)
	}
	janitor, _ := retention.New(db, time.Minute)
	expiredAt := now.Add(-time.Minute)
	_, _ = db.Collection(models.CollectionSituationFacts).InsertOne(ctx, models.SituationFact{PublicID: "expired-fact", OrganizationID: "org", Kind: "incident", Status: "active", ChannelID: "old", MessageTS: "0", ExpiresAt: expiredAt})
	_, _ = db.Collection(models.CollectionDerivations).InsertOne(ctx, models.SourceDerivation{OrganizationID: "org", SourceID: "old-source", DerivedCollection: models.CollectionSituationFacts, DerivedID: "expired-fact", ExpiresAt: expiredAt})
	status, err := janitor.Sweep(ctx)
	if err != nil || status.DeletedDerived != 1 {
		t.Fatalf("retention=%#v err=%v", status, err)
	}
	chain, err := audit.NewMongoChain(db, []byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for index := 0; index < 12; index++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, err := chain.Append(ctx, audit.AppendRequest{OrganizationID: "org", Type: "test", ResourceID: fmt.Sprint(index), RetentionEpoch: "2026-07"})
			errs <- err
		}(index)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := chain.Verify(ctx, "org"); err != nil {
		t.Fatal(err)
	}
	receipts, err := chain.List(ctx, "org")
	if err != nil || len(receipts) != 12 {
		t.Fatalf("receipts=%d err=%v", len(receipts), err)
	}
}

func TestMongoProjectionRejectsStaleOriginalAndPersistsRestriction(t *testing.T) {
	ctx, db := mongoDatabase(t)
	store := observer.NewMongoStore(db, 30*24*time.Hour)
	now := time.Now().UTC()
	original := types.SlackEnvelope{OrganizationID: "org", TeamID: "team", ChannelID: "private-alerts", EnvelopeID: "env-original", EventID: "event-original", MessageTS: "9.0", UserID: "user", Kind: types.SlackEventMessage, Text: "Confidential outage incident is down", EventTime: now}
	accepted, err := store.Accept(ctx, original)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetRestricted(ctx, accepted.Observation.PublicID, true); err != nil {
		t.Fatal(err)
	}
	restrictedObservation := accepted.Observation
	restrictedObservation.Restricted = true
	if _, err := intelligence.NewMongo(db).Project(ctx, restrictedObservation); err != nil {
		t.Fatal(err)
	}
	facts, _ := db.Collection(models.CollectionSituationFacts).CountDocuments(ctx, bson.M{"organization_id": "org"})
	signals, _ := db.Collection(models.CollectionRestrictedSignals).CountDocuments(ctx, bson.M{"organization_id": "org"})
	if facts != 0 || signals != 1 {
		t.Fatalf("restricted projection facts=%d signals=%d", facts, signals)
	}
	deleted := original
	deleted.EnvelopeID, deleted.EventID, deleted.TargetTS = "env-delete", "event-delete", original.MessageTS
	deleted.Kind, deleted.Text, deleted.EventTime = types.SlackEventDelete, "", now.Add(time.Minute)
	if _, err := store.Accept(ctx, deleted); err != nil {
		t.Fatal(err)
	}
	redelivery := original
	redelivery.EnvelopeID, redelivery.EventID = "env-redelivery", "event-redelivery"
	if _, err := store.Accept(ctx, redelivery); err != nil {
		t.Fatal(err)
	}
	message, err := store.CurrentMessage(ctx, "org", "team", "private-alerts", original.MessageTS)
	if err != nil {
		t.Fatal(err)
	}
	if !message.Deleted || message.Text != "" || message.SourceEventID != deleted.EventID {
		t.Fatalf("stale original restored Mongo projection: %#v", message)
	}
}
