package usage

import (
	"context"
	"fmt"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/telemetryos/tos-tag/models"
)

const (
	CategoryClassifier                    = "classifier"
	CategoryClassifierAvoided             = "classifier_avoided"
	ClassifierEfficiencyAccountingVersion = 1
)

// EfficiencyReader exposes content-free classifier accounting. Exact token
// counters are kept separate from estimates derived from the measured daily
// average so operators cannot mistake projected savings for provider usage.
type EfficiencyReader interface {
	ClassifierEfficiency(context.Context, EfficiencyQuery) (EfficiencyReport, error)
}

type EfficiencyQuery struct {
	OrganizationID string
	Since          time.Time
	Until          time.Time
	Location       *time.Location
}

type EfficiencyBucket struct {
	Day                              string           `json:"day"`
	CandidateDecisions               int64            `json:"candidate_decisions"`
	ProviderCalls                    int64            `json:"provider_calls"`
	InstrumentedProviderCalls        int64            `json:"instrumented_provider_calls"`
	UninstrumentedProviderCalls      int64            `json:"uninstrumented_provider_calls"`
	KnownSuccessfulProviderCalls     int64            `json:"known_successful_provider_calls"`
	FailedProviderCalls              int64            `json:"failed_provider_calls"`
	AvoidedProviderCalls             int64            `json:"avoided_provider_calls"`
	MeasuredInputCalls               int64            `json:"measured_input_calls"`
	InputTokens                      int64            `json:"input_tokens"`
	OutputTokens                     int64            `json:"output_tokens"`
	ContextPackTokens                int64            `json:"context_pack_tokens"`
	EstimatedNonContextInputTokens   int64            `json:"estimated_non_context_input_tokens"`
	AverageInputTokens               int64            `json:"average_input_tokens"`
	MaxInputTokens                   int64            `json:"max_input_tokens"`
	SilentProviderRecommendations    int64            `json:"silent_provider_recommendations"`
	EstimatedAvoidedInputTokens      int64            `json:"estimated_avoided_input_tokens"`
	EstimatedPotentialInputTokens    int64            `json:"estimated_potential_input_tokens"`
	EstimatedInputTokenAvoidanceRate float64          `json:"estimated_input_token_avoidance_rate"`
	AvoidedReasons                   map[string]int64 `json:"avoided_reasons,omitempty"`
}

type EfficiencyReport struct {
	OrganizationID string             `json:"organization_id"`
	Timezone       string             `json:"timezone"`
	Since          time.Time          `json:"since"`
	Until          time.Time          `json:"until"`
	GeneratedAt    time.Time          `json:"generated_at"`
	EstimateBasis  string             `json:"estimate_basis"`
	Days           []EfficiencyBucket `json:"days"`
	Totals         EfficiencyBucket   `json:"totals"`
}

func (m *Memory) ClassifierEfficiency(_ context.Context, query EfficiencyQuery) (EfficiencyReport, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	events := make([]Event, 0, len(m.events))
	for _, event := range m.events {
		if event.OrganizationID == query.OrganizationID && !event.CreatedAt.Before(query.Since) && event.CreatedAt.Before(query.Until) {
			events = append(events, event)
		}
	}
	return buildEfficiencyReport(query, events)
}

func (m *Mongo) ClassifierEfficiency(ctx context.Context, query EfficiencyQuery) (EfficiencyReport, error) {
	if err := validateEfficiencyQuery(query); err != nil {
		return EfficiencyReport{}, err
	}
	value := func(field string) bson.M { return bson.M{"$ifNull": bson.A{field, 0}} }
	instrumented := bson.M{"$and": bson.A{
		bson.M{"$eq": bson.A{"$category", CategoryClassifier}},
		bson.M{"$gte": bson.A{value("$efficiency_accounting_version"), ClassifierEfficiencyAccountingVersion}},
	}}
	pipeline := []bson.M{
		{"$match": bson.M{
			"organization_id": query.OrganizationID,
			"category":        bson.M{"$in": bson.A{CategoryClassifier, CategoryClassifierAvoided}},
			"created_at":      bson.M{"$gte": query.Since, "$lt": query.Until},
		}},
		{"$group": bson.M{
			"_id": bson.M{
				"day":    bson.M{"$dateToString": bson.M{"format": "%Y-%m-%d", "date": "$created_at", "timezone": query.Location.String()}},
				"reason": bson.M{"$cond": bson.A{bson.M{"$gt": bson.A{value("$avoided_provider_calls"), 0}}, bson.M{"$ifNull": bson.A{"$reason_code", ""}}, ""}},
			},
			"provider_calls":                bson.M{"$sum": value("$calls")},
			"instrumented_provider_calls":   bson.M{"$sum": bson.M{"$cond": bson.A{instrumented, value("$calls"), 0}}},
			"uninstrumented_provider_calls": bson.M{"$sum": bson.M{"$cond": bson.A{bson.M{"$and": bson.A{bson.M{"$eq": bson.A{"$category", CategoryClassifier}}, bson.M{"$not": bson.A{instrumented}}}}, value("$calls"), 0}}},
			"failed_provider_calls":         bson.M{"$sum": value("$failed_calls")},
			"avoided_provider_calls":        bson.M{"$sum": value("$avoided_provider_calls")},
			"candidate_decisions":           bson.M{"$sum": bson.M{"$add": bson.A{value("$calls"), value("$avoided_provider_calls")}}},
			"input_tokens":                  bson.M{"$sum": value("$input_tokens")},
			"output_tokens":                 bson.M{"$sum": value("$output_tokens")},
			"context_pack_tokens":           bson.M{"$sum": value("$context_pack_tokens")},
			"estimated_non_context_input_tokens": bson.M{"$sum": bson.M{"$cond": bson.A{
				bson.M{"$and": bson.A{bson.M{"$gte": bson.A{value("$efficiency_accounting_version"), ClassifierEfficiencyAccountingVersion}}, bson.M{"$gt": bson.A{value("$input_tokens"), value("$context_pack_tokens")}}}},
				bson.M{"$subtract": bson.A{value("$input_tokens"), value("$context_pack_tokens")}}, 0,
			}}},
			"measured_input_calls":            bson.M{"$sum": bson.M{"$cond": bson.A{bson.M{"$gt": bson.A{value("$input_tokens"), 0}}, 1, 0}}},
			"max_input_tokens":                bson.M{"$max": value("$input_tokens")},
			"silent_provider_recommendations": bson.M{"$sum": bson.M{"$cond": bson.A{bson.M{"$and": bson.A{bson.M{"$eq": bson.A{"$category", CategoryClassifier}}, bson.M{"$eq": bson.A{"$outcome", "silent"}}}}, value("$calls"), 0}}},
		}},
		{"$sort": bson.D{{Key: "_id.day", Value: 1}, {Key: "_id.reason", Value: 1}}},
	}
	cursor, err := m.db.Collection(models.CollectionUsage).Aggregate(ctx, pipeline)
	if err != nil {
		return EfficiencyReport{}, err
	}
	defer cursor.Close(ctx)
	var rows []efficiencyAggregateRow
	if err := cursor.All(ctx, &rows); err != nil {
		return EfficiencyReport{}, err
	}
	return buildEfficiencyReportFromAggregates(query, rows)
}

type efficiencyAggregateRow struct {
	ID struct {
		Day    string `bson:"day"`
		Reason string `bson:"reason"`
	} `bson:"_id"`
	ProviderCalls                  int64 `bson:"provider_calls"`
	InstrumentedProviderCalls      int64 `bson:"instrumented_provider_calls"`
	UninstrumentedProviderCalls    int64 `bson:"uninstrumented_provider_calls"`
	FailedProviderCalls            int64 `bson:"failed_provider_calls"`
	AvoidedProviderCalls           int64 `bson:"avoided_provider_calls"`
	CandidateDecisions             int64 `bson:"candidate_decisions"`
	InputTokens                    int64 `bson:"input_tokens"`
	OutputTokens                   int64 `bson:"output_tokens"`
	ContextPackTokens              int64 `bson:"context_pack_tokens"`
	EstimatedNonContextInputTokens int64 `bson:"estimated_non_context_input_tokens"`
	MeasuredInputCalls             int64 `bson:"measured_input_calls"`
	MaxInputTokens                 int64 `bson:"max_input_tokens"`
	SilentProviderRecommendations  int64 `bson:"silent_provider_recommendations"`
}

func buildEfficiencyReportFromAggregates(query EfficiencyQuery, rows []efficiencyAggregateRow) (EfficiencyReport, error) {
	if err := validateEfficiencyQuery(query); err != nil {
		return EfficiencyReport{}, err
	}
	startDay := dayStart(query.Since, query.Location)
	endDay := dayStart(query.Until.Add(-time.Nanosecond), query.Location)
	buckets := map[string]*EfficiencyBucket{}
	for day := startDay; !day.After(endDay); day = day.AddDate(0, 0, 1) {
		key := day.Format("2006-01-02")
		buckets[key] = &EfficiencyBucket{Day: key}
	}
	totals := EfficiencyBucket{Day: "total"}
	for _, row := range rows {
		bucket := buckets[row.ID.Day]
		if bucket == nil {
			continue
		}
		addEfficiencyAggregate(bucket, row)
		addEfficiencyAggregate(&totals, row)
	}
	days := make([]EfficiencyBucket, 0, len(buckets))
	for _, bucket := range buckets {
		finalizeEfficiencyBucket(bucket)
		days = append(days, *bucket)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Day < days[j].Day })
	finalizeEfficiencyBucket(&totals)
	return EfficiencyReport{
		OrganizationID: query.OrganizationID,
		Timezone:       query.Location.String(),
		Since:          query.Since,
		Until:          query.Until,
		GeneratedAt:    time.Now().UTC(),
		EstimateBasis:  "avoided_provider_calls multiplied by the measured average classifier input tokens in the same bucket; zero when no measured input call exists",
		Days:           days,
		Totals:         totals,
	}, nil
}

func addEfficiencyAggregate(bucket *EfficiencyBucket, row efficiencyAggregateRow) {
	bucket.ProviderCalls += row.ProviderCalls
	bucket.InstrumentedProviderCalls += row.InstrumentedProviderCalls
	bucket.UninstrumentedProviderCalls += row.UninstrumentedProviderCalls
	bucket.FailedProviderCalls += row.FailedProviderCalls
	bucket.AvoidedProviderCalls += row.AvoidedProviderCalls
	bucket.CandidateDecisions += row.CandidateDecisions
	bucket.InputTokens += row.InputTokens
	bucket.OutputTokens += row.OutputTokens
	bucket.ContextPackTokens += row.ContextPackTokens
	bucket.EstimatedNonContextInputTokens += row.EstimatedNonContextInputTokens
	bucket.MeasuredInputCalls += row.MeasuredInputCalls
	bucket.SilentProviderRecommendations += row.SilentProviderRecommendations
	if row.MaxInputTokens > bucket.MaxInputTokens {
		bucket.MaxInputTokens = row.MaxInputTokens
	}
	if row.ID.Reason != "" && row.AvoidedProviderCalls > 0 {
		if bucket.AvoidedReasons == nil {
			bucket.AvoidedReasons = map[string]int64{}
		}
		bucket.AvoidedReasons[row.ID.Reason] += row.AvoidedProviderCalls
	}
}

func buildEfficiencyReport(query EfficiencyQuery, events []Event) (EfficiencyReport, error) {
	if err := validateEfficiencyQuery(query); err != nil {
		return EfficiencyReport{}, err
	}
	location := query.Location
	startDay := dayStart(query.Since, location)
	lastInstant := query.Until.Add(-time.Nanosecond)
	endDay := dayStart(lastInstant, location)
	buckets := map[string]*EfficiencyBucket{}
	for day := startDay; !day.After(endDay); day = day.AddDate(0, 0, 1) {
		key := day.Format("2006-01-02")
		buckets[key] = &EfficiencyBucket{Day: key}
	}
	totals := EfficiencyBucket{Day: "total"}
	for _, event := range events {
		if event.OrganizationID != query.OrganizationID || event.CreatedAt.Before(query.Since) || !event.CreatedAt.Before(query.Until) {
			continue
		}
		if event.Category != CategoryClassifier && event.Category != CategoryClassifierAvoided {
			continue
		}
		key := event.CreatedAt.In(location).Format("2006-01-02")
		bucket := buckets[key]
		if bucket == nil {
			continue
		}
		addEfficiencyEvent(bucket, event)
		addEfficiencyEvent(&totals, event)
	}
	days := make([]EfficiencyBucket, 0, len(buckets))
	for _, bucket := range buckets {
		finalizeEfficiencyBucket(bucket)
		days = append(days, *bucket)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Day < days[j].Day })
	finalizeEfficiencyBucket(&totals)
	return EfficiencyReport{
		OrganizationID: query.OrganizationID,
		Timezone:       location.String(),
		Since:          query.Since,
		Until:          query.Until,
		GeneratedAt:    time.Now().UTC(),
		EstimateBasis:  "avoided_provider_calls multiplied by the measured average classifier input tokens in the same bucket; zero when no measured input call exists",
		Days:           days,
		Totals:         totals,
	}, nil
}

func validateEfficiencyQuery(query EfficiencyQuery) error {
	if query.OrganizationID == "" || query.Location == nil || query.Since.IsZero() || query.Until.IsZero() || !query.Since.Before(query.Until) {
		return fmt.Errorf("invalid classifier efficiency query")
	}
	return nil
}

func dayStart(value time.Time, location *time.Location) time.Time {
	local := value.In(location)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
}

func addEfficiencyEvent(bucket *EfficiencyBucket, event Event) {
	bucket.ProviderCalls += event.Calls
	if event.Category == CategoryClassifier {
		if event.EfficiencyAccountingVersion >= ClassifierEfficiencyAccountingVersion {
			bucket.InstrumentedProviderCalls += event.Calls
		} else {
			bucket.UninstrumentedProviderCalls += event.Calls
		}
	}
	bucket.FailedProviderCalls += event.FailedCalls
	bucket.AvoidedProviderCalls += event.AvoidedProviderCalls
	bucket.CandidateDecisions += event.Calls + event.AvoidedProviderCalls
	bucket.InputTokens += event.InputTokens
	bucket.OutputTokens += event.OutputTokens
	bucket.ContextPackTokens += event.ContextPackTokens
	if event.EfficiencyAccountingVersion >= ClassifierEfficiencyAccountingVersion && event.InputTokens > event.ContextPackTokens {
		bucket.EstimatedNonContextInputTokens += event.InputTokens - event.ContextPackTokens
	}
	if event.InputTokens > 0 {
		bucket.MeasuredInputCalls++
		if event.InputTokens > bucket.MaxInputTokens {
			bucket.MaxInputTokens = event.InputTokens
		}
	}
	if event.Category == CategoryClassifier && event.Outcome == "silent" {
		bucket.SilentProviderRecommendations += event.Calls
	}
	if event.AvoidedProviderCalls > 0 && event.ReasonCode != "" {
		if bucket.AvoidedReasons == nil {
			bucket.AvoidedReasons = map[string]int64{}
		}
		bucket.AvoidedReasons[event.ReasonCode] += event.AvoidedProviderCalls
	}
}

func finalizeEfficiencyBucket(bucket *EfficiencyBucket) {
	bucket.KnownSuccessfulProviderCalls = bucket.InstrumentedProviderCalls - bucket.FailedProviderCalls
	if bucket.KnownSuccessfulProviderCalls < 0 {
		bucket.KnownSuccessfulProviderCalls = 0
	}
	if bucket.MeasuredInputCalls > 0 {
		bucket.AverageInputTokens = bucket.InputTokens / bucket.MeasuredInputCalls
		bucket.EstimatedAvoidedInputTokens = bucket.AverageInputTokens * bucket.AvoidedProviderCalls
	}
	bucket.EstimatedPotentialInputTokens = bucket.InputTokens + bucket.EstimatedAvoidedInputTokens
	if bucket.EstimatedPotentialInputTokens > 0 {
		bucket.EstimatedInputTokenAvoidanceRate = float64(bucket.EstimatedAvoidedInputTokens) / float64(bucket.EstimatedPotentialInputTokens)
	}
}
