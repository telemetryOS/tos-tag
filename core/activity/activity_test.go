package activity

import (
	"testing"
	"unicode/utf8"

	"github.com/RobertWHurst/blackbox"
)

func TestBoundedPreservesUTF8(t *testing.T) {
	got := bounded("ab🙂cd", 4)
	if !utf8.ValidString(got) || got != "ab🙂…" {
		t.Fatalf("bounded UTF-8 = %q", got)
	}
}

func TestHubScopesSnapshotsAndSubscribers(t *testing.T) {
	hub := New(3)
	updates, unsubscribe := hub.Subscribe("org-a")
	defer unsubscribe()
	hub.Publish(Record{OrganizationID: "org-b", Title: "other tenant"})
	hub.Publish(Record{OrganizationID: "org-a", Title: "current tenant"})
	select {
	case record := <-updates:
		if record.Title != "current tenant" {
			t.Fatalf("record=%#v", record)
		}
	default:
		t.Fatal("scoped subscriber received no current-tenant record")
	}
	if records := hub.Snapshot("org-a", 10); len(records) != 1 || records[0].Title != "current tenant" {
		t.Fatalf("snapshot=%#v", records)
	}
}

func TestLogTargetKeepsOnlySafeContext(t *testing.T) {
	hub := New(10)
	hub.Log("logger", blackbox.Info, []any{"classification decision recorded"}, blackbox.Ctx{
		"organization_id": "org", "decision_id": "decision", "duration_ms": int64(10),
		"prompt": "must not appear", "secret": "must not appear",
	}, nil)
	records := hub.Snapshot("org", 10)
	if len(records) != 1 || records[0].Category != "classifier" || records[0].Details["decision_id"] != "decision" {
		t.Fatalf("records=%#v", records)
	}
	if _, exists := records[0].Details["prompt"]; exists {
		t.Fatalf("unsafe context retained: %#v", records[0].Details)
	}
}

func TestCategoryForClassifierRequestDoesNotMislabelStartup(t *testing.T) {
	if got := categoryFor("OpenAI classifier request started", nil); got != "classifier" {
		t.Fatalf("classifier request category=%q", got)
	}
	if got := categoryFor("tos-tag started with live openai classifier", nil); got != "system" {
		t.Fatalf("startup category=%q", got)
	}
	if got := categoryFor("Slack delivery durably completed", map[string]any{"decision_id": "decision", "delivery_id": "delivery"}); got != "delivery" {
		t.Fatalf("delivery category=%q", got)
	}
}

func TestHubRetainsBoundedWindow(t *testing.T) {
	hub := New(2)
	hub.Publish(Record{OrganizationID: "org", Title: "one"})
	hub.Publish(Record{OrganizationID: "org", Title: "two"})
	hub.Publish(Record{OrganizationID: "org", Title: "three"})
	records := hub.Snapshot("org", 10)
	if len(records) != 2 || records[0].Title != "two" || records[1].Title != "three" {
		t.Fatalf("records=%#v", records)
	}
}
