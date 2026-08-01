// Package contextpacks builds deterministic, immutable, source-linked gating
// context without maintaining an organization-wide model session.
package contextpacks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/telemetryos/tos-tag/core/config"
	"github.com/telemetryos/tos-tag/types"
)

var ErrRequiredSourceTooLarge = errors.New("required context source exceeds partition budget")

type Tokenizer interface {
	Revision() string
	Count(string) int
}

type WordTokenizer struct{}

func (WordTokenizer) Revision() string { return "word/v1" }
func (WordTokenizer) Count(value string) int {
	return len(strings.Fields(value))
}

type RuneTokenizer struct{}

func (RuneTokenizer) Revision() string { return "rune/v1" }
func (RuneTokenizer) Count(value string) int {
	return utf8.RuneCountInString(value)
}

type Request struct {
	OrganizationID        string
	TargetObservationID   string
	OrganizationWatermark int64
	PolicyRevision        string
	MembershipRevision    string
	Candidates            []types.ContextCandidate
	CreatedAt             time.Time
	ExpiresAt             time.Time
}

type Builder struct {
	budget    config.ContextPackConfig
	tokenizer Tokenizer
}

func New(budget config.ContextPackConfig, tokenizer Tokenizer) (*Builder, error) {
	if tokenizer == nil {
		return nil, fmt.Errorf("tokenizer is required")
	}
	if budget.MaxTokens <= 0 || budget.PartitionTotal() != budget.MaxTokens {
		return nil, fmt.Errorf("invalid context budget")
	}
	return &Builder{budget: budget, tokenizer: tokenizer}, nil
}

func (b *Builder) Build(request Request) (types.ContextPackRevision, error) {
	if request.OrganizationID == "" || request.TargetObservationID == "" {
		return types.ContextPackRevision{}, fmt.Errorf("organization and target observation are required")
	}
	created := request.CreatedAt.UTC()
	if created.IsZero() {
		created = time.Now().UTC()
	}
	expires := request.ExpiresAt.UTC()
	if expires.IsZero() {
		expires = created.Add(24 * time.Hour)
	}

	grouped := make(map[types.ContextPartition][]types.ContextCandidate)
	for _, candidate := range request.Candidates {
		if candidate.OrganizationID != request.OrganizationID || candidate.ID == "" || strings.TrimSpace(candidate.Text) == "" {
			continue
		}
		if !candidate.SourceExpiresAt.IsZero() && !candidate.SourceExpiresAt.After(created) {
			continue
		}
		grouped[candidate.Partition] = append(grouped[candidate.Partition], candidate)
		if !candidate.SourceExpiresAt.IsZero() && candidate.SourceExpiresAt.Before(expires) {
			expires = candidate.SourceExpiresAt
		}
	}

	ceilings := map[types.ContextPartition]int{
		types.PartitionSystem:    b.budget.System,
		types.PartitionThread:    b.budget.Thread,
		types.PartitionChannel:   b.budget.Channel,
		types.PartitionRecentOrg: b.budget.RecentOrg,
		types.PartitionEvidence:  b.budget.Evidence,
		types.PartitionSituation: b.budget.Situation,
	}
	order := []types.ContextPartition{
		types.PartitionSystem,
		types.PartitionThread,
		types.PartitionChannel,
		types.PartitionRecentOrg,
		types.PartitionEvidence,
		types.PartitionSituation,
	}

	pack := types.ContextPackRevision{
		ID:                    types.RevisionID(types.NewID("ctx")),
		OrganizationID:        request.OrganizationID,
		TargetObservationID:   request.TargetObservationID,
		OrganizationWatermark: request.OrganizationWatermark,
		PolicyRevision:        request.PolicyRevision,
		MembershipRevision:    request.MembershipRevision,
		TokenizerRevision:     b.tokenizer.Revision(),
		PartitionTokens:       make(map[types.ContextPartition]int),
		CreatedAt:             created,
		ExpiresAt:             expires,
	}
	seen := make(map[string]struct{})
	for _, partition := range order {
		candidates := fairOrder(grouped[partition])
		for _, candidate := range candidates {
			key := fmt.Sprintf("%s/%d", candidate.ID, candidate.Version)
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			tokens := b.tokenizer.Count(candidate.Text)
			if tokens <= 0 {
				continue
			}
			if pack.PartitionTokens[partition]+tokens > ceilings[partition] || pack.TotalTokens+tokens > b.budget.MaxTokens-b.budget.Headroom {
				if candidate.Required {
					return types.ContextPackRevision{}, fmt.Errorf("%w: %s", ErrRequiredSourceTooLarge, candidate.ID)
				}
				continue
			}
			pack.Sources = append(pack.Sources, types.ContextSource{
				ID:              candidate.ID,
				Version:         candidate.Version,
				ChannelID:       candidate.ChannelID,
				ChannelName:     candidate.ChannelName,
				AuthorID:        candidate.AuthorID,
				Partition:       partition,
				Provenance:      candidate.Provenance,
				TokenCount:      tokens,
				DisclosureClass: candidate.DisclosureClass,
				Text:            candidate.Text,
				ObservedAt:      candidate.ObservedAt,
			})
			pack.PartitionTokens[partition] += tokens
			pack.TotalTokens += tokens
			seen[key] = struct{}{}
		}
	}
	pack.ContentHash = contentHash(pack)
	return pack, nil
}

func fairOrder(candidates []types.ContextCandidate) []types.ContextCandidate {
	byChannel := make(map[string][]types.ContextCandidate)
	for _, candidate := range candidates {
		channel := candidate.ChannelID
		if channel == "" {
			channel = "~global"
		}
		byChannel[channel] = append(byChannel[channel], candidate)
	}
	channels := make([]string, 0, len(byChannel))
	for channel := range byChannel {
		channels = append(channels, channel)
		sort.SliceStable(byChannel[channel], func(i, j int) bool {
			left, right := byChannel[channel][i], byChannel[channel][j]
			if left.Required != right.Required {
				return left.Required
			}
			if left.Priority != right.Priority {
				return left.Priority > right.Priority
			}
			if !left.ObservedAt.Equal(right.ObservedAt) {
				return left.ObservedAt.After(right.ObservedAt)
			}
			return left.ID < right.ID
		})
	}
	sort.Strings(channels)
	var result []types.ContextCandidate
	for round := 0; ; round++ {
		added := false
		for _, channel := range channels {
			if round < len(byChannel[channel]) {
				result = append(result, byChannel[channel][round])
				added = true
			}
		}
		if !added {
			return result
		}
	}
}

func contentHash(pack types.ContextPackRevision) string {
	canonical := struct {
		OrganizationID        string
		TargetObservationID   string
		OrganizationWatermark int64
		PolicyRevision        string
		MembershipRevision    string
		TokenizerRevision     string
		Sources               []types.ContextSource
	}{pack.OrganizationID, pack.TargetObservationID, pack.OrganizationWatermark, pack.PolicyRevision, pack.MembershipRevision, pack.TokenizerRevision, pack.Sources}
	data, _ := json.Marshal(canonical)
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}
