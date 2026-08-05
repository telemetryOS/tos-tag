package usage

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestUsageContainsOnlyStructuredCounters(t *testing.T) {
	store := NewMemory()
	if err := store.Record(context.Background(), Event{OrganizationID: "o", Category: "model", ProviderID: "openai", ModelID: "gpt", Calls: 1, InputTokens: 10, OutputTokens: 2}); err != nil {
		t.Fatal(err)
	}
	events, err := store.List(context.Background(), "o", 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
}

func TestClassifierEfficiencySeparatesExactUsageFromEstimatedAvoidance(t *testing.T) {
	store := NewMemory()
	location, err := time.LoadLocation("America/Vancouver")
	if err != nil {
		t.Fatal(err)
	}
	events := []Event{
		{OrganizationID: "o", Category: CategoryClassifier, EfficiencyAccountingVersion: ClassifierEfficiencyAccountingVersion, Calls: 1, InputTokens: 100, OutputTokens: 10, ContextPackTokens: 40, Outcome: "silent", CreatedAt: time.Date(2026, 8, 5, 6, 30, 0, 0, time.UTC)},
		{OrganizationID: "o", Category: CategoryClassifier, EfficiencyAccountingVersion: ClassifierEfficiencyAccountingVersion, Calls: 1, InputTokens: 300, OutputTokens: 30, ContextPackTokens: 100, Outcome: "reply_in_thread", CreatedAt: time.Date(2026, 8, 5, 7, 30, 0, 0, time.UTC)},
		{OrganizationID: "o", Category: CategoryClassifier, EfficiencyAccountingVersion: ClassifierEfficiencyAccountingVersion, Calls: 1, FailedCalls: 1, ContextPackTokens: 80, Outcome: "provider_error", CreatedAt: time.Date(2026, 8, 5, 8, 0, 0, 0, time.UTC)},
		{OrganizationID: "o", Category: CategoryClassifierAvoided, EfficiencyAccountingVersion: ClassifierEfficiencyAccountingVersion, AvoidedProviderCalls: 2, Outcome: "silent", ReasonCode: "suppress.deleted", CreatedAt: time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)},
		{OrganizationID: "o", Category: "model", Calls: 1, InputTokens: 999, CreatedAt: time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)},
		{OrganizationID: "other", Category: CategoryClassifier, Calls: 1, InputTokens: 999, CreatedAt: time.Date(2026, 8, 5, 9, 0, 0, 0, time.UTC)},
	}
	for _, event := range events {
		if err := store.Record(context.Background(), event); err != nil {
			t.Fatal(err)
		}
	}
	report, err := store.ClassifierEfficiency(context.Background(), EfficiencyQuery{
		OrganizationID: "o",
		Since:          time.Date(2026, 8, 4, 7, 0, 0, 0, time.UTC),
		Until:          time.Date(2026, 8, 6, 7, 0, 0, 0, time.UTC),
		Location:       location,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Days) != 2 || report.Days[0].Day != "2026-08-04" || report.Days[1].Day != "2026-08-05" {
		t.Fatalf("daily boundaries = %#v", report.Days)
	}
	first := report.Days[0]
	if first.ProviderCalls != 1 || first.InputTokens != 100 || first.SilentProviderRecommendations != 1 {
		t.Fatalf("first day = %#v", first)
	}
	second := report.Days[1]
	if second.CandidateDecisions != 4 || second.ProviderCalls != 2 || second.InstrumentedProviderCalls != 2 || second.UninstrumentedProviderCalls != 0 || second.KnownSuccessfulProviderCalls != 1 || second.FailedProviderCalls != 1 || second.AvoidedProviderCalls != 2 {
		t.Fatalf("second day call accounting = %#v", second)
	}
	if second.InputTokens != 300 || second.OutputTokens != 30 || second.ContextPackTokens != 180 || second.EstimatedNonContextInputTokens != 200 || second.AverageInputTokens != 300 || second.MaxInputTokens != 300 {
		t.Fatalf("second day token accounting = %#v", second)
	}
	if second.EstimatedAvoidedInputTokens != 600 || second.EstimatedPotentialInputTokens != 900 || math.Abs(second.EstimatedInputTokenAvoidanceRate-(2.0/3.0)) > 0.0001 {
		t.Fatalf("second day estimate = %#v", second)
	}
	if second.AvoidedReasons["suppress.deleted"] != 2 {
		t.Fatalf("avoidance reasons = %#v", second.AvoidedReasons)
	}
	if report.Totals.InputTokens != 400 || report.Totals.AverageInputTokens != 200 || report.Totals.EstimatedAvoidedInputTokens != 400 {
		t.Fatalf("totals = %#v", report.Totals)
	}
}

func TestUsageRejectsImpossibleFailureAccounting(t *testing.T) {
	if err := NewMemory().Record(context.Background(), Event{OrganizationID: "o", Category: CategoryClassifier, Calls: 0, FailedCalls: 1}); err == nil {
		t.Fatal("failed calls greater than provider calls were accepted")
	}
}

func TestClassifierEfficiencyKeepsLegacyCoverageExplicit(t *testing.T) {
	store := NewMemory()
	now := time.Now().UTC()
	if err := store.Record(context.Background(), Event{OrganizationID: "o", Category: CategoryClassifier, Calls: 1, InputTokens: 50, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	report, err := store.ClassifierEfficiency(context.Background(), EfficiencyQuery{OrganizationID: "o", Since: now.Add(-time.Minute), Until: now.Add(time.Minute), Location: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	if report.Totals.ProviderCalls != 1 || report.Totals.InstrumentedProviderCalls != 0 || report.Totals.UninstrumentedProviderCalls != 1 || report.Totals.KnownSuccessfulProviderCalls != 0 {
		t.Fatalf("legacy coverage = %#v", report.Totals)
	}
}

func TestClassifierEfficiencyAggregateRowsPreserveDailyAndTotalAccounting(t *testing.T) {
	query := EfficiencyQuery{
		OrganizationID: "o",
		Since:          time.Date(2026, 8, 4, 7, 0, 0, 0, time.UTC),
		Until:          time.Date(2026, 8, 6, 7, 0, 0, 0, time.UTC),
		Location:       time.FixedZone("PDT", -7*60*60),
	}
	measured := efficiencyAggregateRow{
		ProviderCalls: 2, InstrumentedProviderCalls: 2, CandidateDecisions: 2,
		InputTokens: 400, OutputTokens: 40, ContextPackTokens: 140,
		EstimatedNonContextInputTokens: 260, MeasuredInputCalls: 2,
		MaxInputTokens: 300, SilentProviderRecommendations: 1,
	}
	measured.ID.Day = "2026-08-05"
	avoided := efficiencyAggregateRow{AvoidedProviderCalls: 2, CandidateDecisions: 2}
	avoided.ID.Day = "2026-08-05"
	avoided.ID.Reason = "suppress.deleted"
	report, err := buildEfficiencyReportFromAggregates(query, []efficiencyAggregateRow{measured, avoided})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Days) != 2 || report.Days[1].Day != "2026-08-05" {
		t.Fatalf("daily buckets = %#v", report.Days)
	}
	day := report.Days[1]
	if day.ProviderCalls != 2 || day.AvoidedProviderCalls != 2 || day.CandidateDecisions != 4 || day.AverageInputTokens != 200 || day.EstimatedAvoidedInputTokens != 400 || day.MaxInputTokens != 300 || day.AvoidedReasons["suppress.deleted"] != 2 {
		t.Fatalf("daily aggregate = %#v", day)
	}
	if report.Totals.ProviderCalls != day.ProviderCalls || report.Totals.EstimatedPotentialInputTokens != day.EstimatedPotentialInputTokens {
		t.Fatalf("totals = %#v day = %#v", report.Totals, day)
	}
}
