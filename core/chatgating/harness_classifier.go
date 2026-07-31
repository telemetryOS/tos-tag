package chatgating

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/telemetryos/tos-tag/core/harness"
	"github.com/telemetryos/tos-tag/core/modelrouter"
	"github.com/telemetryos/tos-tag/types"
)

type GatingRouter interface {
	Resolve(context.Context, types.ModelRouteContext, modelrouter.Constraints) (types.ResolvedModel, types.DecisionTrace, error)
}

type HarnessClassifier struct {
	harness harness.Harness
	router  GatingRouter
}

func NewHarnessClassifier(agent harness.Harness, router GatingRouter) (*HarnessClassifier, error) {
	if agent == nil || router == nil {
		return nil, errors.New("gating harness and router are required")
	}
	return &HarnessClassifier{harness: agent, router: router}, nil
}

func (c *HarnessClassifier) Decide(ctx context.Context, target Target, pack types.ContextPackRevision) (types.ChatGatingDecision, error) {
	resolved, _, err := c.router.Resolve(ctx, types.ModelRouteContext{OrganizationID: target.Envelope.OrganizationID, WorkspaceID: target.Envelope.TeamID, ChannelID: target.Envelope.ChannelID, Phase: "gating", DataClasses: []string{"internal"}, Capabilities: []string{"structured"}, InputTokens: pack.TotalTokens}, modelrouter.Constraints{})
	if err != nil {
		return types.ChatGatingDecision{}, err
	}
	payload := struct {
		Message      string                  `json:"message"`
		Mode         types.ParticipationMode `json:"mode"`
		ActiveThread bool                    `json:"active_thread"`
		Sources      []types.ContextSource   `json:"sources"`
	}{target.Envelope.Text, target.Mode, target.ActiveThread, pack.Sources}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return types.ChatGatingDecision{}, err
	}
	session, err := c.harness.CreateSession(ctx, "tos-tag gating "+target.ObservationID)
	if err != nil {
		return types.ChatGatingDecision{}, err
	}
	system := `You are a tool-free Slack response gate. Decide whether the agent should remain silent or act using only the supplied immutable sources. Return exactly one JSON object with fields: outcome, confidence, reason_codes, topic_ids, releasable_evidence_ids, restricted_signal_ids, response_intent, disclosure_class, requires_full_agent. Allowed outcomes are silent, react, reply_in_thread, reply_in_channel, start_background_job, escalate_for_approval. Evidence IDs must exactly match supplied source IDs. Restricted-awareness sources can appear only in restricted_signal_ids and can never ground final prose. Default to silent on ambiguity or social chatter. A destination-safe source that states current operational status directly answers an explicit operational-status question; in assist or proactive mode, select reply_in_thread when active_thread is true and otherwise reply_in_channel, and include that source ID in releasable_evidence_ids. Do not select react when source-grounded prose is needed to answer a question. Example: for message "Is the system down?", mode "assist", active_thread false, and destination-safe source {"id":"alerts/1","text":"Active production outage"}, return {"outcome":"reply_in_channel","confidence":0.99,"reason_codes":["direct_question","cross_channel_incident_evidence"],"topic_ids":["outage"],"releasable_evidence_ids":["alerts/1"],"restricted_signal_ids":[],"response_intent":"answer_status","disclosure_class":"destination_safe","requires_full_agent":true}. Do not use tools and do not include chain-of-thought.`
	if err := c.harness.Prompt(ctx, session.ID, harness.Prompt{Text: string(encoded), System: system, Model: resolved.ProviderID + "/" + resolved.ModelID, Variant: resolved.Variant, RequestID: "gate-" + target.ObservationID, SlackFormat: "json/gating-v1"}); err != nil {
		_ = c.harness.Abort(context.Background(), session.ID)
		return types.ChatGatingDecision{}, err
	}
	events, errs := c.harness.Events(ctx, session.ID)
	var output strings.Builder
	for events != nil || errs != nil {
		select {
		case event, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			if event.Type == "message.delta" {
				if text, ok := event.Data["text"].(string); ok {
					output.WriteString(text)
				}
			}
		case eventErr, ok := <-errs:
			if !ok {
				errs = nil
				continue
			}
			if eventErr != nil {
				return types.ChatGatingDecision{}, eventErr
			}
		case <-ctx.Done():
			_ = c.harness.Abort(context.Background(), session.ID)
			return types.ChatGatingDecision{}, ctx.Err()
		}
	}
	raw := strings.TrimSpace(output.String())
	raw = strings.TrimPrefix(raw, "```json")
	raw = strings.TrimPrefix(raw, "```")
	raw = strings.TrimSuffix(raw, "```")
	raw = strings.TrimSpace(raw)
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	var decision types.ChatGatingDecision
	if err := decoder.Decode(&decision); err != nil {
		return types.ChatGatingDecision{}, fmt.Errorf("decode gating decision: %w", err)
	}
	return decision, nil
}

var _ Classifier = (*HarnessClassifier)(nil)
