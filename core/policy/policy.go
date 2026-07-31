// Package policy provides deterministic deny-wins authorization outside models.
package policy

import "strings"

type Effect string

const (
	EffectAllow           Effect = "allow"
	EffectDeny            Effect = "deny"
	EffectRequireApproval Effect = "require_approval"
)

type Input struct {
	OrganizationID string
	PrincipalID    string
	RequesterID    string
	Ambient        bool
	Operation      string
	Risk           string
	Destination    string
	DataClass      string
}

type Rule struct {
	ID                string
	Effect            Effect
	OrganizationID    string
	PrincipalID       string
	RequesterID       string
	OperationPrefix   string
	Risk              string
	DestinationPrefix string
	DataClass         string
	Priority          int
}

type Decision struct {
	Effect       Effect   `json:"effect"`
	Reason       string   `json:"reason"`
	MatchedRules []string `json:"matched_rules"`
}

type Engine struct{ rules []Rule }

func New(rules []Rule) *Engine { return &Engine{rules: append([]Rule(nil), rules...)} }

func (e *Engine) Evaluate(input Input) Decision {
	if input.OrganizationID == "" || input.PrincipalID == "" || input.Operation == "" {
		return Decision{Effect: EffectDeny, Reason: "policy.incomplete_input"}
	}
	if input.Ambient && isWriteRisk(input.Risk) {
		return Decision{Effect: EffectDeny, Reason: "policy.ambient_write_denied"}
	}
	decision := Decision{Effect: EffectDeny, Reason: "policy.default_deny"}
	highestAllow, highestApproval := -1, -1
	for _, rule := range e.rules {
		if !matches(rule, input) {
			continue
		}
		decision.MatchedRules = append(decision.MatchedRules, rule.ID)
		if rule.Effect == EffectDeny {
			return Decision{Effect: EffectDeny, Reason: "policy.explicit_deny", MatchedRules: decision.MatchedRules}
		}
		if rule.Effect == EffectAllow && rule.Priority > highestAllow {
			highestAllow = rule.Priority
		}
		if rule.Effect == EffectRequireApproval && rule.Priority > highestApproval {
			highestApproval = rule.Priority
		}
	}
	if highestApproval >= highestAllow && highestApproval >= 0 {
		decision.Effect, decision.Reason = EffectRequireApproval, "policy.approval_required"
	} else if highestAllow >= 0 {
		decision.Effect, decision.Reason = EffectAllow, "policy.allowed"
	}
	return decision
}

func matches(rule Rule, input Input) bool {
	return (rule.OrganizationID == "" || rule.OrganizationID == input.OrganizationID) &&
		(rule.PrincipalID == "" || rule.PrincipalID == input.PrincipalID) &&
		(rule.RequesterID == "" || rule.RequesterID == input.RequesterID) &&
		(rule.OperationPrefix == "" || strings.HasPrefix(input.Operation, rule.OperationPrefix)) &&
		(rule.Risk == "" || rule.Risk == input.Risk) &&
		(rule.DestinationPrefix == "" || strings.HasPrefix(input.Destination, rule.DestinationPrefix)) &&
		(rule.DataClass == "" || rule.DataClass == input.DataClass)
}

func isWriteRisk(risk string) bool {
	return risk == "write" || risk == "destructive" || risk == "admin"
}
