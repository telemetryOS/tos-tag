package deliveries

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/telemetryos/tos-tag/types"
)

func TestSlackOutputPromptContainsRequiredContract(t *testing.T) {
	required := []string{"<https://example.com|descriptive label>", "*bold*", "_italic_", "single backticks", "triple-backtick", "complete table", "Do not choose or alter"}
	for _, value := range required {
		if !strings.Contains(SlackOutputPrompt, value) {
			t.Errorf("prompt missing %q", value)
		}
	}
	withBase := WithSlackOutputContract("base safety")
	if !strings.HasPrefix(withBase, "base safety") || !strings.Contains(withBase, SlackOutputPrompt) {
		t.Fatal("output contract was not appended to system instructions")
	}
}

func TestRendererRendersMRKDWNAndNativeTable(t *testing.T) {
	result := types.SlackResult{Segments: []types.SlackSegment{
		{Kind: types.SlackSegmentMRKDWN, Text: "*Status:* use `JOB-123` and <https://example.com|open receipt>."},
		{Kind: types.SlackSegmentTable, Table: &types.SlackTable{
			Columns: []types.SlackTableColumn{{Header: "ID"}, {Header: "Count", Align: "right"}},
			Rows:    [][]types.SlackTableCell{{{Type: "raw_text", Text: "JOB-123"}, {Type: "raw_number", Number: 2}}},
		}},
	}}
	payloads, err := NewRenderer().Render(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 1 || len(payloads[0].Blocks) != 2 {
		t.Fatalf("unexpected payloads: %#v", payloads)
	}
	if payloads[0].Blocks[1]["type"] != "table" {
		t.Fatalf("expected native table block, got %#v", payloads[0].Blocks[1])
	}
}

func TestRendererRejectsNonSlackFormattingAndBroadcasts(t *testing.T) {
	cases := []string{
		"[link](https://example.com)",
		"**not slack bold**",
		"<!channel> hello",
		"<@U12345> hello",
		"<#C12345|alerts> hello",
		"<!subteam^S12345> hello",
		"```unterminated",
		"<javascript:alert(1)|bad>",
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			_, err := NewRenderer().Render(types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentMRKDWN, Text: input}}})
			if !errors.Is(err, ErrInvalidResult) {
				t.Fatalf("got %v, want invalid result", err)
			}
		})
	}
}

func TestRendererSplitsLargeNativeTable(t *testing.T) {
	rows := make([][]types.SlackTableCell, 150)
	for i := range rows {
		rows[i] = []types.SlackTableCell{{Type: "raw_text", Text: fmt.Sprintf("row-%03d", i)}}
	}
	result := types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentTable, Table: &types.SlackTable{
		Columns: []types.SlackTableColumn{{Header: "Row"}},
		Rows:    rows,
	}}}}
	payloads, err := NewRenderer().Render(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 2 {
		t.Fatalf("expected two payloads, got %d", len(payloads))
	}
	for _, payload := range payloads {
		if len(payload.Blocks) != 1 || payload.Blocks[0]["type"] != "table" {
			t.Fatalf("bad split payload: %#v", payload)
		}
	}
}

func TestRendererRejectsMismatchedTable(t *testing.T) {
	_, err := NewRenderer().Render(types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentTable, Table: &types.SlackTable{
		Columns: []types.SlackTableColumn{{Header: "A"}, {Header: "B"}},
		Rows:    [][]types.SlackTableCell{{{Text: "only one"}}},
	}}}})
	if !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("got %v, want invalid result", err)
	}
}
