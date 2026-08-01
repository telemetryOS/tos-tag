package deliveries

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/telemetryos/tos-tag/types"
)

// ParseModelOutput converts the model-owned output boundary into the typed
// Slack result owned by tos-tag. Plain text remains a safe mrkdwn fallback for
// providers that do not support structured output. JSON-looking output must
// satisfy the typed contract instead of being posted literally.
func ParseModelOutput(output string) (types.SlackResult, error) {
	raw := strings.TrimSpace(output)
	if raw == "" {
		return types.SlackResult{}, fmt.Errorf("empty model output")
	}
	raw = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(raw, "```json"), "```"), "```"))
	if !strings.HasPrefix(raw, "{") && !strings.HasPrefix(raw, "[") {
		return types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentMRKDWN, Text: raw}}}, nil
	}

	var result types.SlackResult
	if strings.HasPrefix(raw, "{") {
		if err := json.Unmarshal([]byte(raw), &result); err == nil && len(result.Segments) > 0 {
			if modelSegmentsAllowed(result.Segments) {
				return result, nil
			}
			return types.SlackResult{}, fmt.Errorf("model output cannot emit privileged Slack segments")
		}
		var legacy struct {
			Type     types.SlackSegmentKind `json:"type"`
			Kind     types.SlackSegmentKind `json:"kind"`
			Text     string                 `json:"text"`
			Table    *types.SlackTable      `json:"table"`
			Artifact *types.SlackArtifact   `json:"artifact"`
			Notice   *types.SlackNotice     `json:"notice"`
		}
		if err := json.Unmarshal([]byte(raw), &legacy); err == nil {
			kind := legacy.Kind
			if kind == "" {
				kind = legacy.Type
			}
			if kind != "" && kind != types.SlackSegmentApproval && kind != types.SlackSegmentNotice && legacy.Notice == nil {
				return types.SlackResult{Segments: []types.SlackSegment{{Kind: kind, Text: legacy.Text, Table: legacy.Table, Artifact: legacy.Artifact}}}, nil
			}
		}
	}
	var segments []types.SlackSegment
	if err := json.Unmarshal([]byte(raw), &segments); err == nil && len(segments) > 0 {
		if modelSegmentsAllowed(segments) {
			return types.SlackResult{Segments: segments}, nil
		}
		return types.SlackResult{}, fmt.Errorf("model output cannot emit privileged Slack segments")
	}
	return types.SlackResult{}, fmt.Errorf("model output violates %s", SlackOutputContractVersion)
}

func modelSegmentsAllowed(segments []types.SlackSegment) bool {
	for _, segment := range segments {
		if segment.Kind == types.SlackSegmentApproval || segment.Approval != nil || segment.Kind == types.SlackSegmentNotice || segment.Notice != nil {
			return false
		}
	}
	return true
}
