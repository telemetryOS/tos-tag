// Package modelrouter resolves named profiles through deterministic precedence
// while applying hard constraints before preferences.
package modelrouter

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/telemetryos/tos-tag/types"
)

var ErrNoEligibleModel = errors.New("no eligible model profile")

type Rule struct {
	ID             string `json:"id" bson:"id"`
	OrganizationID string `json:"organization_id,omitempty" bson:"organization_id,omitempty"`
	WorkspaceID    string `json:"workspace_id,omitempty" bson:"workspace_id,omitempty"`
	ChannelID      string `json:"channel_id,omitempty" bson:"channel_id,omitempty"`
	RoutineID      string `json:"routine_id,omitempty" bson:"routine_id,omitempty"`
	SkillName      string `json:"skill_name,omitempty" bson:"skill_name,omitempty"`
	Phase          string `json:"phase,omitempty" bson:"phase,omitempty"`
	ProfileID      string `json:"profile_id" bson:"profile_id"`
	Priority       int    `json:"priority,omitempty" bson:"priority,omitempty"`
}

type Constraints struct {
	AllowedProviders  map[string]bool
	DisabledProfiles  map[string]bool
	AvailableProfiles map[string]bool
	MaxInputTokens    int
}

type Router struct {
	profiles             map[string]types.ModelProfile
	rules                []Rule
	organizationDefaults map[string]string
	deploymentDefault    string
	policyRevision       string
}

func New(profiles []types.ModelProfile, rules []Rule, organizationDefaults map[string]string, deploymentDefault, policyRevision string) (*Router, error) {
	indexed := make(map[string]types.ModelProfile, len(profiles))
	for _, profile := range profiles {
		if profile.ID == "" || profile.ProviderID == "" || profile.ModelID == "" || profile.MaxInputTokens <= 0 {
			return nil, fmt.Errorf("invalid model profile %q", profile.ID)
		}
		if _, duplicate := indexed[profile.ID]; duplicate {
			return nil, fmt.Errorf("duplicate model profile %q", profile.ID)
		}
		indexed[profile.ID] = profile
	}
	if _, ok := indexed[deploymentDefault]; !ok {
		return nil, fmt.Errorf("deployment default profile %q is missing", deploymentDefault)
	}
	return &Router{profiles: indexed, rules: append([]Rule(nil), rules...), organizationDefaults: organizationDefaults, deploymentDefault: deploymentDefault, policyRevision: policyRevision}, nil
}

func (r *Router) Resolve(_ context.Context, route types.ModelRouteContext, constraints Constraints) (types.ResolvedModel, types.DecisionTrace, error) {
	candidates, matched := r.candidates(route)
	trace := types.DecisionTrace{MatchedRule: matched}
	seen := make(map[string]struct{})
	var queue []string
	for _, candidate := range candidates {
		if _, ok := seen[candidate]; ok || candidate == "" {
			continue
		}
		seen[candidate] = struct{}{}
		queue = append(queue, candidate)
	}
	for len(queue) > 0 {
		profileID := queue[0]
		queue = queue[1:]
		profile, ok := r.profiles[profileID]
		if !ok {
			trace.Rejected = append(trace.Rejected, profileID+":missing")
			continue
		}
		trace.Tried = append(trace.Tried, profileID)
		if reason := eligible(profile, route, constraints); reason != "" {
			trace.Rejected = append(trace.Rejected, profileID+":"+reason)
			for _, fallback := range profile.FallbackProfileIDs {
				if _, duplicate := seen[fallback]; !duplicate {
					seen[fallback] = struct{}{}
					queue = append(queue, fallback)
				}
			}
			continue
		}
		trace.Fallback = len(trace.Tried) > 1
		return types.ResolvedModel{ProfileID: profile.ID, ProviderID: profile.ProviderID, ModelID: profile.ModelID, Variant: profile.Variant, PolicyRev: r.policyRevision}, trace, nil
	}
	return types.ResolvedModel{}, trace, ErrNoEligibleModel
}

func (r *Router) candidates(route types.ModelRouteContext) ([]string, string) {
	if route.Override != "" {
		return []string{route.Override}, "override"
	}
	type match struct {
		profile  string
		rank     int
		priority int
		id       string
	}
	var matches []match
	for _, rule := range r.rules {
		if rule.OrganizationID != "" && rule.OrganizationID != route.OrganizationID {
			continue
		}
		if rule.WorkspaceID != "" && rule.WorkspaceID != route.WorkspaceID {
			continue
		}
		if rule.ChannelID != "" && rule.ChannelID != route.ChannelID {
			continue
		}
		if rule.RoutineID != "" && rule.RoutineID != route.RoutineID {
			continue
		}
		if rule.Phase != "" && rule.Phase != route.Phase {
			continue
		}
		if rule.SkillName != "" && rule.SkillName != route.SkillName {
			continue
		}
		rank := 0
		switch {
		case rule.Phase != "" || rule.SkillName != "":
			rank = 6
		case rule.RoutineID != "":
			rank = 5
		case rule.ChannelID != "":
			rank = 4
		case rule.WorkspaceID != "":
			rank = 3
		default:
			rank = 2
		}
		matches = append(matches, match{rule.ProfileID, rank, rule.Priority, rule.ID})
	}
	// Channel defaults are operator-managed durable configuration and may outlive
	// a profile catalog change. Treat a missing profile as stale configuration,
	// not as an explicit override that blocks the deployment fallback. Valid but
	// disabled/ineligible profiles still fail closed through the normal eligibility
	// checks below.
	if _, exists := r.profiles[route.ChannelDefault]; route.ChannelDefault != "" && exists {
		matches = append(matches, match{profile: route.ChannelDefault, rank: 4, id: "channel_default"})
	}
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].rank != matches[j].rank {
			return matches[i].rank > matches[j].rank
		}
		if matches[i].priority != matches[j].priority {
			return matches[i].priority > matches[j].priority
		}
		return matches[i].id < matches[j].id
	})
	if len(matches) > 0 {
		result := make([]string, len(matches))
		for i, value := range matches {
			result[i] = value.profile
		}
		return result, matches[0].id
	}
	if profile := r.organizationDefaults[route.OrganizationID]; profile != "" {
		return []string{profile, r.deploymentDefault}, "organization_default"
	}
	return []string{r.deploymentDefault}, "deployment_default"
}

func eligible(profile types.ModelProfile, route types.ModelRouteContext, constraints Constraints) string {
	if !profile.Enabled || constraints.DisabledProfiles[profile.ID] {
		return "disabled"
	}
	if constraints.AvailableProfiles != nil && !constraints.AvailableProfiles[profile.ID] {
		return "unavailable"
	}
	if constraints.AllowedProviders != nil && !constraints.AllowedProviders[profile.ProviderID] {
		return "provider_denied"
	}
	if route.InputTokens > profile.MaxInputTokens || (constraints.MaxInputTokens > 0 && route.InputTokens > constraints.MaxInputTokens) {
		return "context_too_large"
	}
	for _, required := range route.Capabilities {
		if !contains(profile.RequiredCapabilities, required) {
			return "capability_missing"
		}
	}
	for _, dataClass := range route.DataClasses {
		if !contains(profile.AllowedDataClasses, dataClass) {
			return "data_class_denied"
		}
	}
	return ""
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
