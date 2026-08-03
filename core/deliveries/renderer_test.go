package deliveries

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/telemetryos/tos-tag/types"
)

func TestSlackOutputPromptContainsRequiredContract(t *testing.T) {
	required := []string{"header, mrkdwn_text", "context, divider, table, card, carousel, image, or artifact", "every explicit part of the request", "Never leave a heading, label, or trailing colon", "Never add model, reasoning effort, token usage, or latency metadata", "trusted runtime measurements", "sortable, paginated Data Table", "Never put actions", "published durable document or download", "Keep short and medium answers in Slack", "Agent Wiki artifacts namespace", "20,000 visible characters", "soft planning signal, not a hard cutoff", "exact HTTPS URL returned by the tool", "Never fabricate, predict, or reconstruct a Wiki URL", "no Wiki artifact was created", "at most one optional header", "no more than two short sentences", "Omit tool narration", "<https://example.com|descriptive label>", "Wiki get or url", "Never expose a namespace/slug", "*bold*", "_italic_", "single backticks", "literal identifier containing an underscore", "byte-for-byte", "reply_in_channel to replyinchannel", "triple-backtick", "complete table", "Do not preface or follow", "Never emit actions", "Do not choose or alter"}
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

func TestRendererRendersSafePresentationPalette(t *testing.T) {
	result := types.SlackResult{Segments: []types.SlackSegment{
		{Kind: types.SlackSegmentHeader, Text: "Deployment report"},
		{Kind: types.SlackSegmentMRKDWN, Text: "*Status:* passed"},
		{Kind: types.SlackSegmentContext, Text: "QA • updated `14:32 UTC`"},
		{Kind: types.SlackSegmentDivider},
		{Kind: types.SlackSegmentImage, Image: &types.SlackImage{URL: "https://example.com/chart.png", AltText: "Latency by hour", Title: "Latency trend"}},
		{Kind: types.SlackSegmentArtifact, Artifact: &types.SlackArtifact{Name: "report.json", MediaType: "application/json", URL: "https://example.com/report.json"}},
	}}
	payloads, err := NewRenderer().Render(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 1 {
		t.Fatalf("payload count = %d", len(payloads))
	}
	wantTypes := []string{"header", "section", "context", "divider", "image", "section"}
	for index, wantType := range wantTypes {
		if payloads[0].Blocks[index]["type"] != wantType {
			t.Fatalf("block %d type = %v, want %s", index, payloads[0].Blocks[index]["type"], wantType)
		}
	}
	if !strings.Contains(payloads[0].Text, "Latency by hour") {
		t.Fatalf("fallback text does not describe image: %q", payloads[0].Text)
	}
}

func TestRendererAppendsFullAgentExecutionFooter(t *testing.T) {
	result := types.SlackResult{
		Segments: []types.SlackSegment{{Kind: types.SlackSegmentMRKDWN, Text: "The investigation is complete."}},
		AgentFooter: &types.SlackAgentFooter{
			ModelID: "gpt-5.6-luna", ReasoningEffort: "max", InputTokens: 21_000,
			OutputTokens: 1_200, TotalTokens: 22_200, DurationMS: 12_450,
		},
	}
	payloads, err := NewRenderer().Render(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 1 || len(payloads[0].Blocks) != 2 {
		t.Fatalf("footer payloads = %#v", payloads)
	}
	footer := payloads[0].Blocks[1]
	if footer["type"] != "context" || footer["block_id"] != "tos_tag_agent_footer" {
		t.Fatalf("footer block = %#v", footer)
	}
	encoded, _ := json.Marshal(footer)
	for _, expected := range []string{"ChatGPT 5.6 Luna", "max effort", "22k tokens", "12.4s"} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("footer %s missing %q", encoded, expected)
		}
	}
}

func TestRendererOmitsExecutionFooterFromUnattributedReplies(t *testing.T) {
	result := types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentMRKDWN, Text: "You're welcome!"}}}
	payloads, err := NewRenderer().Render(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 1 || len(payloads[0].Blocks) != 1 || strings.Contains(payloads[0].Text, "tokens") {
		t.Fatalf("classifier-only reply gained an execution footer: %#v", payloads)
	}
	encoded, err := json.Marshal(types.SlackResult{Segments: result.Segments, AgentFooter: &types.SlackAgentFooter{ModelID: "private-model", TotalTokens: 1}})
	if err != nil || strings.Contains(string(encoded), "private-model") || strings.Contains(string(encoded), "agent_footer") {
		t.Fatalf("control-plane footer leaked into model JSON: %s err=%v", encoded, err)
	}
}

func TestArtifactSegmentRequiresSameAttemptToolProvenance(t *testing.T) {
	result := types.SlackResult{Segments: []types.SlackSegment{{
		Kind: types.SlackSegmentArtifact,
		Artifact: &types.SlackArtifact{
			Name: "Architecture guide", MediaType: "text/html", URL: "https://wiki.example/artifacts/architecture-guide",
		},
	}}}
	if err := ValidateArtifactProvenance(result, nil); !errors.Is(err, ErrInvalidResult) || ValidationCode(err) != "artifact_unverified" {
		t.Fatalf("unverified artifact error = %v code=%q", err, ValidationCode(err))
	}
	if err := ValidateArtifactProvenance(result, map[string]struct{}{"https://wiki.example/artifacts/architecture-guide": {}}); err != nil {
		t.Fatalf("tool-produced artifact was rejected: %v", err)
	}
}

func TestPublishedArtifactSummaryIsCompactAndCanonical(t *testing.T) {
	artifactURL := "https://wiki.example/artifacts/architecture-guide"
	result := types.SlackResult{Segments: []types.SlackSegment{
		{Kind: types.SlackSegmentHeader, Text: "Architecture published"},
		{Kind: types.SlackSegmentMRKDWN, Text: "A concise synopsis.\n\n<" + artifactURL + "|Open it>"},
		{Kind: types.SlackSegmentContext, Text: "Revision 3"},
		{Kind: types.SlackSegmentDivider},
		{Kind: types.SlackSegmentMRKDWN, Text: "A repeated copy of the document body."},
		{Kind: types.SlackSegmentMRKDWN, Text: "The link again: <" + artifactURL + "|open>."},
	}}

	compacted := CompactPublishedArtifactSummary(result, map[string]struct{}{artifactURL: {}})
	if len(compacted.Segments) != 3 {
		t.Fatalf("segments = %#v", compacted.Segments)
	}
	if compacted.Segments[0].Kind != types.SlackSegmentHeader || compacted.Segments[1].Text != "A concise synopsis." {
		t.Fatalf("summary segments = %#v", compacted.Segments[:2])
	}
	artifact := compacted.Segments[2]
	if artifact.Kind != types.SlackSegmentArtifact || artifact.Artifact == nil || artifact.Artifact.URL != artifactURL || artifact.Artifact.MediaType != "text/html" {
		t.Fatalf("artifact segment = %#v", artifact)
	}
	if err := ValidateArtifactProvenance(compacted, map[string]struct{}{artifactURL: {}}); err != nil {
		t.Fatal(err)
	}
}

func TestPublishedArtifactSummaryDropsWorkerChatterAndRepeatedLinks(t *testing.T) {
	artifactURL := "https://wiki.example/artifacts/architecture-guide"
	chatty := "Published revision 3 covering ingestion, privacy-scoped context, classification, workers, reviewed tools, and Slack delivery. Confirmed runtime facts are separated from unconfirmed details.\n\n<" + artifactURL + "|Open the architecture reference>\n\nWiki write succeeded and was verified with a same-attempt read. The document is ready for future operators. End of publication summary."
	result := types.SlackResult{Segments: []types.SlackSegment{
		{Kind: types.SlackSegmentHeader, Text: "Architecture reference published"},
		{Kind: types.SlackSegmentMRKDWN, Text: chatty},
		{Kind: types.SlackSegmentContext, Text: "Wiki write succeeded and was verified"},
		{Kind: types.SlackSegmentMRKDWN, Text: "Artifact link: <" + artifactURL + "|open>"},
	}}

	compacted := CompactPublishedArtifactSummary(result, map[string]struct{}{artifactURL: {}})
	if len(compacted.Segments) != 3 {
		t.Fatalf("segments = %#v", compacted.Segments)
	}
	if got := compacted.Segments[1].Text; got != "Published revision 3 covering ingestion, privacy-scoped context, classification, workers, reviewed tools, and Slack delivery. Confirmed runtime facts are separated from unconfirmed details." {
		t.Fatalf("synopsis = %q", got)
	}
	encoded, _ := json.Marshal(compacted)
	if bytes.Contains(encoded, []byte("same-attempt")) || bytes.Count(encoded, []byte(artifactURL)) != 1 {
		t.Fatalf("chatty or repeated artifact output survived: %s", encoded)
	}
}

func TestRendererRejectsUnsafePresentationSegments(t *testing.T) {
	cases := []types.SlackSegment{
		{Kind: types.SlackSegmentHeader, Text: strings.Repeat("h", maxHeaderCharacters+1)},
		{Kind: types.SlackSegmentContext, Text: "<!channel>"},
		{Kind: types.SlackSegmentDivider, Text: "payload"},
		{Kind: types.SlackSegmentImage, Image: &types.SlackImage{URL: "http://example.com/chart.png", AltText: "Chart"}},
		{Kind: types.SlackSegmentImage, Image: &types.SlackImage{URL: "https://example.com/chart.png"}},
	}
	for _, segment := range cases {
		if _, err := NewRenderer().Render(types.SlackResult{Segments: []types.SlackSegment{segment}}); !errors.Is(err, ErrInvalidResult) {
			t.Fatalf("segment %#v error = %v, want invalid result", segment, err)
		}
	}
	if _, err := NewRenderer().Render(types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentDivider}}}); !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("divider-only result error = %v, want invalid result", err)
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
	if payloads[0].Blocks[0]["expand"] != true {
		t.Fatalf("AI section should be expanded: %#v", payloads[0].Blocks[0])
	}
	rows := payloads[0].Blocks[1]["rows"].([]any)
	number := rows[1].([]any)[1].(map[string]any)
	if number["value"] != float64(2) || number["text"] != "2" {
		t.Fatalf("raw number cell = %#v, want Slack value and display text", number)
	}
}

func TestRendererRendersModelSafeCardAndCarousel(t *testing.T) {
	result := types.SlackResult{Segments: []types.SlackSegment{
		{Kind: types.SlackSegmentCard, Card: &types.SlackCard{Title: "Deployment", Subtitle: "QA", Body: "*Healthy* across all checks.", Subtext: "Updated now", Icon: &types.SlackCardImage{URL: "https://example.com/icon.png", AltText: "Deployment icon"}}},
		{Kind: types.SlackSegmentCarousel, Carousel: &types.SlackCarousel{Cards: []types.SlackCard{
			{Title: "Node Mini", Body: "Best for compact single-screen deployments."},
			{Title: "Node Pro", Body: "Best for demanding multi-output workloads.", HeroImage: &types.SlackCardImage{URL: "https://example.com/pro.png", AltText: "Node Pro"}},
		}}},
	}}
	payloads, err := NewRenderer().Render(result)
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 1 || len(payloads[0].Blocks) != 2 || payloads[0].Blocks[0]["type"] != "card" || payloads[0].Blocks[1]["type"] != "carousel" {
		t.Fatalf("native card rendering = %#v", payloads)
	}
	if _, present := payloads[0].Blocks[0]["actions"]; present {
		t.Fatalf("model-safe card unexpectedly has actions: %#v", payloads[0].Blocks[0])
	}
	if !strings.Contains(payloads[0].Text, "Node Mini") || !strings.Contains(payloads[0].Text, "Healthy") {
		t.Fatalf("card fallback text = %q", payloads[0].Text)
	}
}

func TestRendererRejectsUnsafeCardAndCarousel(t *testing.T) {
	cases := []types.SlackSegment{
		{Kind: types.SlackSegmentCard, Card: &types.SlackCard{Title: "Card", Body: strings.Repeat("x", maxCardTextCharacters+1)}},
		{Kind: types.SlackSegmentCard, Card: &types.SlackCard{Title: "Card", Body: "Body", Icon: &types.SlackCardImage{URL: "http://example.com/icon.png", AltText: "Icon"}}},
		{Kind: types.SlackSegmentCarousel, Carousel: &types.SlackCarousel{Cards: make([]types.SlackCard, maxCarouselCards+1)}},
	}
	for _, segment := range cases {
		if _, err := NewRenderer().Render(types.SlackResult{Segments: []types.SlackSegment{segment}}); !errors.Is(err, ErrInvalidResult) {
			t.Fatalf("unsafe segment %#v rendered with error %v", segment, err)
		}
	}
}

func TestRendererUsesDataTableForCaptionedDataset(t *testing.T) {
	result := types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentTable, Table: &types.SlackTable{
		Caption: "Service latency", PageSize: 5, RowHeaderColumnIndex: 0,
		Columns: []types.SlackTableColumn{{Header: "Service"}, {Header: "P95 ms"}},
		Rows:    [][]types.SlackTableCell{{{Type: "raw_text", Text: "gateway"}, {Type: "raw_number", Number: 42}}},
	}}}}
	payloads, err := NewRenderer().Render(result)
	if err != nil {
		t.Fatal(err)
	}
	block := payloads[0].Blocks[0]
	if block["type"] != "data_table" || block["caption"] != "Service latency" || block["page_size"] != 5 {
		t.Fatalf("data table block = %#v", block)
	}
	rows := block["rows"].([]any)
	if len(rows) != 2 || rows[0].([]any)[0].(map[string]any)["type"] != "raw_text" || rows[1].([]any)[1].(map[string]any)["value"] != float64(42) {
		t.Fatalf("data table rows = %#v", rows)
	}
}

func TestRendererPromotesFormattedTableCellsToRichText(t *testing.T) {
	result := types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentTable, Table: &types.SlackTable{
		Columns: []types.SlackTableColumn{{Header: "Option"}, {Header: "Recommendation"}},
		Rows: [][]types.SlackTableCell{{
			{Type: "raw_text", Text: "Node Mini"},
			{Type: "raw_text", Text: "*Recommended* for `4K`. <https://example.com/mini|Details>"},
		}},
	}}}}
	payloads, err := NewRenderer().Render(result)
	if err != nil {
		t.Fatal(err)
	}
	rows := payloads[0].Blocks[0]["rows"].([]any)
	cell := rows[1].([]any)[1].(map[string]any)
	if cell["type"] != "rich_text" {
		t.Fatalf("formatted raw cell was not promoted: %#v", cell)
	}
	encoded, err := json.Marshal(cell)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{`"bold":true`, `"code":true`, `"type":"link"`, `"url":"https://example.com/mini"`} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("rich cell missing %s: %s", expected, encoded)
		}
	}
	if strings.Contains(string(encoded), "<https://") || strings.Contains(string(encoded), "*Recommended*") {
		t.Fatalf("rich cell retained literal markup: %s", encoded)
	}
}

func TestRendererRejectsMentionsInsideRawTableCells(t *testing.T) {
	_, err := NewRenderer().Render(types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentTable, Table: &types.SlackTable{
		Columns: []types.SlackTableColumn{{Header: "Unsafe"}},
		Rows:    [][]types.SlackTableCell{{{Type: "raw_text", Text: "<!channel>"}}},
	}}}})
	if !errors.Is(err, ErrInvalidResult) || ValidationCode(err) != "mrkdwn_forbidden_mention" {
		t.Fatalf("raw table mention error=%v code=%q", err, ValidationCode(err))
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

func TestRendererRequiresLinksForWikiReferences(t *testing.T) {
	for name, text := range map[string]string{
		"primer slug":       "Source: Agent Wiki Primer, `primer/02-hardware/node-mini/io-capabilities`.",
		"artifact slug":     "See `artifacts/tos-tag-architecture` for the full report.",
		"named Wiki source": "Agent Wiki source: `finance/premium-trial`.",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewRenderer().Render(types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentMRKDWN, Text: text}}})
			if !errors.Is(err, ErrInvalidResult) || ValidationCode(err) != "wiki_reference_slug" {
				t.Fatalf("error = %v, code = %q", err, ValidationCode(err))
			}
		})
	}
	cardResult := types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentCard, Card: &types.SlackCard{Title: "Node Mini", Body: "Agent Wiki source: `primer/node-mini`."}}}}
	if _, err := NewRenderer().Render(cardResult); !errors.Is(err, ErrInvalidResult) || ValidationCode(err) != "wiki_reference_slug" {
		t.Fatalf("card Wiki slug error=%v code=%q", err, ValidationCode(err))
	}

	for name, text := range map[string]string{
		"Slack link": "Source: <https://wiki.example/pages/opaque-id|Agent Wiki Primer: Node Mini I/O>.",
		"raw URL":    "Source: https://wiki.example/pages/opaque-id",
		"code path":  "The implementation is in `core/pipeline/pipeline.go`.",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NewRenderer().Render(types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentMRKDWN, Text: text}}}); err != nil {
				t.Fatalf("linked or non-Wiki reference rejected: %v", err)
			}
		})
	}
}

func TestResolveWikiReferenceLinksUsesOnlySameAttemptResolvedURL(t *testing.T) {
	const reference = "primer/02-hardware/node-mini/io-capabilities"
	fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(reference)))
	result := types.SlackResult{Segments: []types.SlackSegment{{
		Kind: types.SlackSegmentMRKDWN,
		Text: "Source: Agent Wiki Primer, `" + reference + "`.",
	}}}

	resolved := ResolveWikiReferenceLinks(result, map[string]string{fingerprint: "https://wiki.example/pages/opaque-id"})
	if got := resolved.Segments[0].Text; got != "Source: Agent Wiki Primer, <https://wiki.example/pages/opaque-id|Agent Wiki source>." {
		t.Fatalf("resolved text = %q", got)
	}
	if _, err := NewRenderer().Render(resolved); err != nil {
		t.Fatalf("resolved Wiki reference rejected: %v", err)
	}

	unresolved := ResolveWikiReferenceLinks(result, map[string]string{"different": "https://wiki.example/pages/other"})
	if _, err := NewRenderer().Render(unresolved); !errors.Is(err, ErrInvalidResult) || ValidationCode(err) != "wiki_reference_slug" {
		t.Fatalf("unresolved reference error = %v, code = %q", err, ValidationCode(err))
	}

	alreadyLinked := types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentMRKDWN, Text: "<https://wiki.example/pages/opaque-id|" + reference + ">"}}}
	if got := ResolveWikiReferenceLinks(alreadyLinked, map[string]string{fingerprint: "https://wiki.example/pages/opaque-id"}).Segments[0].Text; got != alreadyLinked.Segments[0].Text {
		t.Fatalf("existing Slack link changed to %q", got)
	}
}

func TestRendererAllowsOnlyControlPlaneAttributedMentions(t *testing.T) {
	result := types.SlackResult{
		Segments:        []types.SlackSegment{{Kind: types.SlackSegmentMRKDWN, Text: "<@U_TOM> reported checkout down in <#C_DEV>."}},
		AllowedMentions: types.SlackMentionAllowlist{UserIDs: []string{"U_TOM"}, ChannelIDs: []string{"C_DEV"}},
	}
	payloads, err := NewRenderer().Render(result)
	if err != nil || len(payloads) != 1 {
		t.Fatalf("attributed mentions were rejected: payloads=%#v err=%v", payloads, err)
	}
	for _, unsafe := range []string{"<@U_OTHER>", "<#C_OTHER>", "<!channel>", "<!here>", "<!subteam^S123>"} {
		result.Segments[0].Text = unsafe
		if _, err := NewRenderer().Render(result); !errors.Is(err, ErrInvalidResult) || ValidationCode(err) != "mrkdwn_forbidden_mention" {
			t.Fatalf("unattributed mention %q error=%v code=%q", unsafe, err, ValidationCode(err))
		}
	}
}

func TestValidationCodeIsStableAndContentFree(t *testing.T) {
	tests := []struct {
		result types.SlackResult
		want   string
	}{
		{types.SlackResult{}, "no_segments"},
		{types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentMRKDWN, Text: "**bad**"}}}, "mrkdwn_double_asterisk"},
		{types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentTable, Table: &types.SlackTable{Columns: []types.SlackTableColumn{{Header: "A"}, {Header: "B"}}, Rows: [][]types.SlackTableCell{{{Text: "secret-value"}}}}}}}, "table_row_shape"},
	}
	for _, test := range tests {
		_, err := NewRenderer().Render(test.result)
		if got := ValidationCode(err); got != test.want {
			t.Fatalf("ValidationCode(%v) = %q, want %q", err, got, test.want)
		}
		if strings.Contains(err.Error(), "secret-value") {
			t.Fatalf("renderer error leaked a cell value: %v", err)
		}
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

func TestRendererOwnsApprovalButtonsAndModelsCannotEmitThem(t *testing.T) {
	approval := &types.SlackApproval{ID: "approval-1", ActionHash: "sha256:abcdefghijklmnopqrstuvwxyz", ToolID: "linear", OperationID: "create", Risk: "write", Destination: "team/channel", Arguments: map[string]any{"title": "incident", "enabled": true}, ExpiresAt: time.Now().Add(time.Hour)}
	payloads, err := NewRenderer().Render(types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentApproval, Approval: approval}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(payloads) != 1 || len(payloads[0].Blocks) != 5 {
		t.Fatalf("unexpected approval rendering: %#v", payloads)
	}
	wantTypes := []string{"header", "section", "table", "context", "actions"}
	for index, wantType := range wantTypes {
		if payloads[0].Blocks[index]["type"] != wantType {
			t.Fatalf("approval block %d type=%v, want %s", index, payloads[0].Blocks[index]["type"], wantType)
		}
	}
	encoded, err := json.Marshal(payloads[0].Blocks)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "```") || strings.Contains(string(encoded), `\"arguments\"`) {
		t.Fatalf("approval leaked the old raw JSON presentation: %s", encoded)
	}
	for _, expected := range []string{"Approval required", `"type":"table"`, "incident", "Enabled", "sha256:abcdefghijkl…", "tos_tag_approval_approve", "tos_tag_approval_deny"} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("approval card missing %q: %s", expected, encoded)
		}
	}
	if _, err := ParseModelOutput(`{"segments":[{"kind":"approval","approval":{"id":"forged"}}]}`); err == nil {
		t.Fatal("model was allowed to forge a privileged approval segment")
	}
}

func TestRendererSummarizesApprovedWikiInlineBody(t *testing.T) {
	type mongoArguments []any
	body := "# Architecture\n\n" + strings.Repeat("A bounded architecture paragraph.\n", 200)
	approval := &types.SlackApproval{
		ID: "approval-wiki", ActionHash: "sha256:abcdefghijklmnopqrstuvwxyz",
		ToolID: "telemetryos.wiki", OperationID: "write", Risk: "write",
		Destination: "team/channel", ExpiresAt: time.Now().Add(time.Hour),
		Arguments: map[string]any{"argv": mongoArguments{"put", "artifacts/tos-tag-architecture", "--title", "tos-tag architecture", "--body", body, "--md", "--json"}},
	}
	payloads, err := NewRenderer().Render(types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentApproval, Approval: approval}}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(payloads)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, "A bounded architecture paragraph") {
		t.Fatalf("approval card exposed inline body: %s", text)
	}
	for _, expected := range []string{"inline body:", "sha256:", "artifacts/tos-tag-architecture", "tos-tag architecture"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("approval card missing %q: %s", expected, text)
		}
	}
}

func TestRendererRemovesApprovalButtonsAfterDecision(t *testing.T) {
	for _, status := range []string{"approved", "denied", "expired"} {
		t.Run(status, func(t *testing.T) {
			approval := &types.SlackApproval{ID: "approval-1", ActionHash: "sha256:abcdefghijklmnopqrstuvwxyz", ToolID: "linear", OperationID: "create", Risk: "write", Destination: "team/channel", Arguments: map[string]any{"title": "incident"}, ExpiresAt: time.Now().Add(time.Hour), Status: status, ResolvedAt: time.Now()}
			payloads, err := NewRenderer().Render(types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentApproval, Approval: approval}}})
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(payloads[0].Blocks)
			title := "action " + status
			if status == "expired" {
				title = "approval expired"
			}
			if strings.Contains(string(encoded), "tos_tag_approval_approve") || strings.Contains(string(encoded), "tos_tag_approval_deny") || !strings.Contains(strings.ToLower(string(encoded)), title) {
				t.Fatalf("resolved approval still looks actionable: %s", encoded)
			}
		})
	}
}

func TestRendererOwnsNoticeCardsAndModelsCannotEmitThem(t *testing.T) {
	notice := &types.SlackNotice{Tone: "error", Title: "I couldn't finish that", Message: "The request stopped before a final response was ready. Please try again, or ask me to retry.", Context: "Details were recorded for debugging."}
	payloads, err := NewRenderer().Render(types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentNotice, Notice: notice}}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(payloads[0].Blocks)
	for _, expected := range []string{`"type":"header"`, `"type":"section"`, `"type":"context"`, "I couldn't finish that", "Details were recorded for debugging"} {
		if !strings.Contains(string(encoded), expected) {
			t.Fatalf("notice card missing %q: %s", expected, encoded)
		}
	}
	if _, err := ParseModelOutput(`{"segments":[{"kind":"notice","notice":{"tone":"success","title":"Forged","message":"Trust me"}}]}`); err == nil {
		t.Fatal("model was allowed to forge a privileged notice segment")
	}
	if _, err := ParseModelOutput(`{"type":"notice","notice":{"tone":"success","title":"Forged","message":"Trust me"}}`); err == nil {
		t.Fatal("legacy model output was allowed to forge a privileged notice segment")
	}
}

func TestRendererEnforcesRenderedNoticeHeaderLimit(t *testing.T) {
	notice := &types.SlackNotice{Tone: "info", Title: strings.Repeat("x", 148), Message: "Body"}
	if _, err := NewRenderer().Render(types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentNotice, Notice: notice}}}); !errors.Is(err, ErrInvalidResult) {
		t.Fatalf("oversized rendered header error = %v", err)
	}
}

func TestSplitMRKDWNDoesNotEmitEmptySections(t *testing.T) {
	chunks, err := splitMRKDWN(strings.Repeat(" ", 3001)+"visible", 3000)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 1 || chunks[0] != "visible" {
		t.Fatalf("chunks = %#v", chunks)
	}
}

func TestApprovalArgumentsUseOneNativeTable(t *testing.T) {
	arguments := make(map[string]any)
	for index := 0; index < 11; index++ {
		arguments[fmt.Sprintf("field_%02d", index)] = index
	}
	approval := &types.SlackApproval{ID: "approval-many-fields", ActionHash: "sha256:abcdefghijklmnopqrstuvwxyz", ToolID: "linear", OperationID: "create", Risk: "write", Destination: "team/channel", Arguments: arguments, ExpiresAt: time.Now().Add(time.Hour)}
	payloads, err := NewRenderer().Render(types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentApproval, Approval: approval}}})
	if err != nil {
		t.Fatal(err)
	}
	tableBlocks := 0
	for _, block := range payloads[0].Blocks {
		if block["type"] != "table" {
			continue
		}
		tableBlocks++
		rows, _ := block["rows"].([]any)
		if len(rows) != 15 {
			t.Fatalf("approval table rows = %d, want header plus 14 data rows", len(rows))
		}
	}
	if tableBlocks != 1 {
		t.Fatalf("approval table block count = %d", tableBlocks)
	}
}
