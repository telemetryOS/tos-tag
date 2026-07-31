package deliveries

import (
	"testing"

	"github.com/telemetryos/tos-tag/types"
)

func TestParseModelOutputSupportsCanonicalLegacyAndPlainText(t *testing.T) {
	for name, input := range map[string]string{
		"canonical": `{"segments":[{"kind":"mrkdwn_text","text":"READY"}]}`,
		"legacy":    `{"type":"mrkdwn_text","text":"READY"}`,
		"plain":     `READY`,
	} {
		t.Run(name, func(t *testing.T) {
			result, err := ParseModelOutput(input)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Segments) != 1 || result.Segments[0].Kind != types.SlackSegmentMRKDWN || result.Segments[0].Text != "READY" {
				t.Fatalf("result = %#v", result)
			}
			if _, err := NewRenderer().Render(result); err != nil {
				t.Fatalf("parsed result did not render: %v", err)
			}
		})
	}
}

func TestParseModelOutputRejectsJSONLookingContractViolation(t *testing.T) {
	if _, err := ParseModelOutput(`{"message":"do not post this object"}`); err == nil {
		t.Fatal("malformed structured output was accepted")
	}
}
