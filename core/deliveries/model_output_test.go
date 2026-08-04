package deliveries

import (
	"errors"
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

func TestParseModelOutputPreservesPlainTextCodeFences(t *testing.T) {
	input := "Run this:\n```go\nfmt.Println(\"ready\")\n```"
	result, err := ParseModelOutput(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Segments) != 1 || result.Segments[0].Text != input {
		t.Fatalf("plain-text fences changed: %#v", result.Segments)
	}
	if _, err := NewRenderer().Render(result); err != nil {
		t.Fatalf("preserved fenced response did not render: %v", err)
	}

	fencedJSON := "```json\n{\"segments\":[{\"kind\":\"mrkdwn_text\",\"text\":\"READY\"}]}\n```"
	result, err = ParseModelOutput(fencedJSON)
	if err != nil || len(result.Segments) != 1 || result.Segments[0].Text != "READY" {
		t.Fatalf("fenced JSON was not decoded: result=%#v err=%v", result, err)
	}
}

func TestParseModelOutputNormalizesGitHubBoldWithoutChangingCode(t *testing.T) {
	result, err := ParseModelOutput("{\"segments\":[{\"kind\":\"mrkdwn_text\",\"text\":\"**Healthy** with `value ** 2`\\n\\n```python\\nvalue ** 3\\n```\"}]}")
	if err != nil {
		t.Fatal(err)
	}
	want := "*Healthy* with `value ** 2`\n\n```python\nvalue ** 3\n```"
	if len(result.Segments) != 1 || result.Segments[0].Text != want {
		t.Fatalf("normalized text = %q", result.Segments[0].Text)
	}
	if _, err := NewRenderer().Render(result); err != nil {
		t.Fatalf("normalized result did not render: %v", err)
	}
}

func TestParseModelOutputPromotesMarkdownTableToNativeSegment(t *testing.T) {
	result, err := ParseModelOutput(`{"segments":[{"kind":"mrkdwn_text","text":"Summary first.\n\n| Boundary | Owner |\n|---|:---:|\n| Classifier | Go control plane |\n| Worker | Codex App Server |\n\nClosing note."}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Segments) != 3 || result.Segments[0].Kind != types.SlackSegmentMRKDWN || result.Segments[1].Kind != types.SlackSegmentTable || result.Segments[2].Kind != types.SlackSegmentMRKDWN {
		t.Fatalf("segments = %#v", result.Segments)
	}
	table := result.Segments[1].Table
	if table == nil || len(table.Columns) != 2 || len(table.Rows) != 2 || table.Columns[1].Align != "center" || table.Rows[0][1].Text != "Go control plane" {
		t.Fatalf("table = %#v", table)
	}
	payloads, err := NewRenderer().Render(result)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, payload := range payloads {
		for _, block := range payload.Blocks {
			found = found || block["type"] == "table"
		}
	}
	if !found {
		t.Fatalf("native table block missing: %#v", payloads)
	}
}

func TestParseModelOutputNormalizesTypedTableRowShapes(t *testing.T) {
	result, err := ParseModelOutput(`{"segments":[{"kind":"table","table":{"columns":[{"header":"Product"},{"header":"Power"},{"header":"Notes"}],"rows":[[{"text":"Mini"},{"text":"12V"}],[{"text":"Pro"},{"text":"PoE"},{"text":"Managed"},{"text":"Optional adapter"}]]}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Segments) != 1 || result.Segments[0].Table == nil {
		t.Fatalf("result = %#v", result)
	}
	rows := result.Segments[0].Table.Rows
	if len(rows[0]) != 3 || rows[0][2].Text != "" {
		t.Fatalf("short row = %#v", rows[0])
	}
	if len(rows[1]) != 3 || rows[1][2].Text != "Managed · Optional adapter" {
		t.Fatalf("long row = %#v", rows[1])
	}
	if _, err := NewRenderer().Render(result); err != nil {
		t.Fatalf("normalized table did not render: %v", err)
	}
}

func TestParseModelOutputDegradesUnsafeSlackLinksToVisibleLabels(t *testing.T) {
	result, err := ParseModelOutput(`{"segments":[{"kind":"mrkdwn_text","text":"See <file:///workspace/core/jobs/state.go|core/jobs/state.go> and <https://example.com/retry|the retry guide>."}]}`)
	if err != nil {
		t.Fatal(err)
	}
	want := "See core/jobs/state.go and <https://example.com/retry|the retry guide>."
	if len(result.Segments) != 1 || result.Segments[0].Text != want {
		t.Fatalf("normalized text = %q", result.Segments[0].Text)
	}
	if _, err := NewRenderer().Render(result); err != nil {
		t.Fatalf("normalized links did not render: %v", err)
	}
}

func TestParseModelOutputLeavesPipeTextAndFencedTablesAlone(t *testing.T) {
	for name, input := range map[string]string{
		"ordinary pipe prose": `{"segments":[{"kind":"mrkdwn_text","text":"Choose A | B when appropriate."}]}`,
		"fenced literal":      "{\"segments\":[{\"kind\":\"mrkdwn_text\",\"text\":\"```text\\n| A | B |\\n|---|---|\\n| 1 | 2 |\\n```\"}]}",
	} {
		t.Run(name, func(t *testing.T) {
			result, err := ParseModelOutput(input)
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Segments) != 1 || result.Segments[0].Kind != types.SlackSegmentMRKDWN {
				t.Fatalf("segments = %#v", result.Segments)
			}
		})
	}
}

func TestParseModelOutputRejectsJSONLookingContractViolation(t *testing.T) {
	if _, err := ParseModelOutput(`{"message":"do not post this object"}`); err == nil {
		t.Fatal("malformed structured output was accepted")
	}
}

func TestModelOutputCannotSelfAuthorizeSlackMentions(t *testing.T) {
	result, err := ParseModelOutput(`{"segments":[{"kind":"mrkdwn_text","text":"<@U_OTHER> hello"}],"allowed_mentions":{"user_ids":["U_OTHER"]}}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.AllowedMentions.UserIDs) != 0 || len(result.AllowedMentions.ChannelIDs) != 0 {
		t.Fatalf("model populated control-plane mention provenance: %#v", result.AllowedMentions)
	}
	if _, err := NewRenderer().Render(result); !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("self-authorized model mention rendered: %v", err)
	}
}

func TestParseModelOutputSupportsRicherTypedPalette(t *testing.T) {
	result, err := ParseModelOutput(`{"segments":[{"kind":"header","text":"Report"},{"kind":"card","card":{"title":"Deployment","body":"Healthy"}},{"kind":"carousel","carousel":{"cards":[{"title":"A","body":"First"},{"title":"B","body":"Second"}]}},{"kind":"divider"},{"kind":"image","image":{"url":"https://example.com/chart.png","alt_text":"A chart"}}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Segments) != 5 || result.Segments[0].Kind != types.SlackSegmentHeader || result.Segments[1].Card == nil || result.Segments[2].Carousel == nil || result.Segments[4].Image == nil {
		t.Fatalf("result = %#v", result)
	}
	if _, err := NewRenderer().Render(result); err != nil {
		t.Fatalf("parsed palette did not render: %v", err)
	}
}

func TestParseModelOutputRecoversCanonicalResultAfterPreamble(t *testing.T) {
	result, err := ParseModelOutput(`I will prepare the Slack response now.{"segments":[{"kind":"header","text":"Recovered"},{"kind":"mrkdwn_text","text":"No raw JSON."}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Segments) != 2 || result.Segments[0].Kind != types.SlackSegmentHeader || result.Segments[0].Text != "Recovered" {
		t.Fatalf("result = %#v", result)
	}
	if _, err := NewRenderer().Render(result); err != nil {
		t.Fatalf("recovered result did not render: %v", err)
	}
}

func TestParseModelOutputRejectsMalformedOrPrivilegedCanonicalSuffixAfterPreamble(t *testing.T) {
	for name, output := range map[string]string{
		"malformed":  `Planning.{"segments":"not-an-array"}`,
		"privileged": `Planning.{"segments":[{"kind":"approval","approval":{"id":"forged"}}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseModelOutput(output); err == nil {
				t.Fatal("invalid canonical suffix was accepted")
			}
		})
	}
}
