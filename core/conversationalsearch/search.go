// Package conversationalsearch exposes bounded, source-linked organization
// memory only across the requester/principal/destination audience intersection.
package conversationalsearch

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/telemetryos/tos-tag/models"
)

type Source interface {
	Recent(context.Context, string, []string, time.Time, int) ([]models.ChannelMessage, error)
}

type Request struct {
	OrganizationID     string
	Query              string
	TargetChannelID    string
	RequesterChannels  []string
	PrincipalChannels  []string
	AudienceChannels   []string
	MembershipRevision string
	MembershipStale    bool
	Since              time.Time
	Limit              int
}

type Result struct {
	SourceID  string    `json:"source_id"`
	ChannelID string    `json:"channel_id"`
	MessageTS string    `json:"message_ts"`
	Text      string    `json:"text"`
	Observed  time.Time `json:"observed_at"`
}

type Searcher struct{ source Source }

func New(source Source) (*Searcher, error) {
	if source == nil {
		return nil, errors.New("search source is required")
	}
	return &Searcher{source: source}, nil
}

func (s *Searcher) Search(ctx context.Context, request Request) ([]Result, error) {
	if request.OrganizationID == "" || request.TargetChannelID == "" || request.MembershipRevision == "" || request.Limit <= 0 || request.Limit > 100 {
		return nil, errors.New("invalid search scope or bound")
	}
	channels := intersect(request.RequesterChannels, request.PrincipalChannels, request.AudienceChannels)
	if request.MembershipStale {
		if contains(channels, request.TargetChannelID) {
			channels = []string{request.TargetChannelID}
		} else {
			return nil, errors.New("stale membership denies cross-channel search")
		}
	}
	if len(channels) == 0 {
		return []Result{}, nil
	}
	since := request.Since
	if since.IsZero() {
		since = time.Now().UTC().Add(-30 * 24 * time.Hour)
	}
	messages, err := s.source.Recent(ctx, request.OrganizationID, channels, since, request.Limit*4)
	if err != nil {
		return nil, err
	}
	terms := strings.Fields(strings.ToLower(request.Query))
	results := make([]Result, 0, request.Limit)
	for index := len(messages) - 1; index >= 0 && len(results) < request.Limit; index-- {
		message := messages[index]
		if !matchesTerms(strings.ToLower(message.Text), terms) {
			continue
		}
		results = append(results, Result{SourceID: message.ChannelID + "/" + message.MessageTS, ChannelID: message.ChannelID, MessageTS: message.MessageTS, Text: message.Text, Observed: message.OriginalAt})
	}
	return results, nil
}

func intersect(groups ...[]string) []string {
	counts := make(map[string]int)
	for _, group := range groups {
		seen := make(map[string]bool)
		for _, value := range group {
			if value != "" && !seen[value] {
				seen[value] = true
				counts[value]++
			}
		}
	}
	result := make([]string, 0)
	for value, count := range counts {
		if count == len(groups) {
			result = append(result, value)
		}
	}
	sort.Strings(result)
	return result
}

func matchesTerms(text string, terms []string) bool {
	for _, term := range terms {
		if !strings.Contains(text, term) {
			return false
		}
	}
	return true
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
