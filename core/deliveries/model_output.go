package deliveries

import (
	"encoding/json"
	"fmt"
	"net/url"
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
	raw = unwrapJSONFence(raw)
	if index := strings.Index(raw, `{"segments"`); index > 0 {
		// Some harnesses leak a short planning/status sentence before the exact
		// structured result. Recover the typed suffix instead of posting its JSON
		// literally. The normal privileged-segment checks still apply below.
		raw = strings.TrimSpace(raw[index:])
	}
	if !strings.HasPrefix(raw, "{") && !strings.HasPrefix(raw, "[") {
		return promoteMarkdownTables(normalizeModelSlackMarkdown(types.SlackResult{Segments: []types.SlackSegment{{Kind: types.SlackSegmentMRKDWN, Text: raw}}})), nil
	}

	var result types.SlackResult
	if strings.HasPrefix(raw, "{") {
		if err := json.Unmarshal([]byte(raw), &result); err == nil && len(result.Segments) > 0 {
			if modelSegmentsAllowed(result.Segments) {
				return normalizeModelSlackResult(result), nil
			}
			return types.SlackResult{}, fmt.Errorf("model output cannot emit privileged Slack segments")
		}
		var legacy struct {
			Type     types.SlackSegmentKind `json:"type"`
			Kind     types.SlackSegmentKind `json:"kind"`
			Text     string                 `json:"text"`
			Table    *types.SlackTable      `json:"table"`
			Card     *types.SlackCard       `json:"card"`
			Carousel *types.SlackCarousel   `json:"carousel"`
			Image    *types.SlackImage      `json:"image"`
			Artifact *types.SlackArtifact   `json:"artifact"`
			Notice   *types.SlackNotice     `json:"notice"`
		}
		if err := json.Unmarshal([]byte(raw), &legacy); err == nil {
			kind := legacy.Kind
			if kind == "" {
				kind = legacy.Type
			}
			if kind != "" && kind != types.SlackSegmentApproval && kind != types.SlackSegmentNotice && legacy.Notice == nil {
				return normalizeModelSlackResult(types.SlackResult{Segments: []types.SlackSegment{{Kind: kind, Text: legacy.Text, Table: legacy.Table, Card: legacy.Card, Carousel: legacy.Carousel, Image: legacy.Image, Artifact: legacy.Artifact}}}), nil
			}
		}
	}
	var segments []types.SlackSegment
	if err := json.Unmarshal([]byte(raw), &segments); err == nil && len(segments) > 0 {
		if modelSegmentsAllowed(segments) {
			return normalizeModelSlackResult(types.SlackResult{Segments: segments}), nil
		}
		return types.SlackResult{}, fmt.Errorf("model output cannot emit privileged Slack segments")
	}
	return types.SlackResult{}, fmt.Errorf("model output violates %s", SlackOutputContractVersion)
}

func unwrapJSONFence(raw string) string {
	trimmed := strings.TrimSpace(raw)
	prefix := ""
	switch {
	case strings.HasPrefix(trimmed, "```json"):
		prefix = "```json"
	case strings.HasPrefix(trimmed, "```"):
		prefix = "```"
	default:
		return trimmed
	}
	if !strings.HasSuffix(trimmed, "```") || len(trimmed) == len(prefix) {
		return trimmed
	}
	inner := strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(trimmed, prefix), "```"))
	if strings.HasPrefix(inner, "{") || strings.HasPrefix(inner, "[") {
		return inner
	}
	return trimmed
}

func normalizeModelSlackResult(result types.SlackResult) types.SlackResult {
	return promoteMarkdownTables(normalizeModelTableRows(normalizeModelSlackLinks(normalizeModelSlackMarkdown(result))))
}

// normalizeModelSlackLinks keeps provider-generated local paths and malformed
// link targets from invalidating an entire otherwise safe response. HTTP(S)
// links remain clickable; unsupported Slack-style targets degrade to their
// visible label. The renderer still validates every surviving link.
func normalizeModelSlackLinks(result types.SlackResult) types.SlackResult {
	normalize := func(text string) string {
		return slackLinkPattern.ReplaceAllStringFunc(text, func(candidate string) string {
			match := slackLinkPattern.FindStringSubmatch(candidate)
			if len(match) != 3 {
				return candidate
			}
			parsed, err := url.Parse(match[1])
			if err == nil && (parsed.Scheme == "https" || parsed.Scheme == "http") && parsed.Host != "" {
				return candidate
			}
			if label := strings.TrimSpace(match[2]); label != "" {
				return label
			}
			return strings.TrimSpace(match[1])
		})
	}
	for segmentIndex := range result.Segments {
		segment := &result.Segments[segmentIndex]
		segment.Text = normalize(segment.Text)
		if segment.Table != nil {
			for rowIndex := range segment.Table.Rows {
				for cellIndex := range segment.Table.Rows[rowIndex] {
					segment.Table.Rows[rowIndex][cellIndex].Text = normalize(segment.Table.Rows[rowIndex][cellIndex].Text)
				}
			}
		}
		if segment.Card != nil {
			segment.Card.Title = normalize(segment.Card.Title)
			segment.Card.Subtitle = normalize(segment.Card.Subtitle)
			segment.Card.Body = normalize(segment.Card.Body)
			segment.Card.Subtext = normalize(segment.Card.Subtext)
		}
		if segment.Carousel != nil {
			for cardIndex := range segment.Carousel.Cards {
				card := &segment.Carousel.Cards[cardIndex]
				card.Title = normalize(card.Title)
				card.Subtitle = normalize(card.Subtitle)
				card.Body = normalize(card.Body)
				card.Subtext = normalize(card.Subtext)
			}
		}
	}
	return result
}

// normalizeModelTableRows repairs a common typed-output mistake without
// discarding an otherwise useful answer. Slack requires every native table row
// to have exactly the declared number of columns. Missing cells are padded;
// surplus cells are folded into the final column so their content remains
// visible. The renderer still applies every normal cell, size, link, mention,
// and formatting validation after this model-boundary normalization.
func normalizeModelTableRows(result types.SlackResult) types.SlackResult {
	for segmentIndex := range result.Segments {
		table := result.Segments[segmentIndex].Table
		if result.Segments[segmentIndex].Kind != types.SlackSegmentTable || table == nil || len(table.Columns) == 0 {
			continue
		}
		columnCount := len(table.Columns)
		for rowIndex := range table.Rows {
			row := table.Rows[rowIndex]
			switch {
			case len(row) < columnCount:
				table.Rows[rowIndex] = append(row, make([]types.SlackTableCell, columnCount-len(row))...)
			case len(row) > columnCount:
				last := row[columnCount-1]
				for _, extra := range row[columnCount:] {
					if extra.Text != "" {
						if last.Text != "" {
							last.Text += " · "
						}
						last.Text += extra.Text
					}
				}
				normalized := append([]types.SlackTableCell(nil), row[:columnCount]...)
				normalized[columnCount-1] = last
				table.Rows[rowIndex] = normalized
			}
		}
	}
	return result
}

// normalizeModelSlackMarkdown repairs the common GitHub-Markdown bold form at
// the untrusted model boundary. Literal code is preserved byte-for-byte; the
// renderer still validates the normalized result and all mention/link rules.
func normalizeModelSlackMarkdown(result types.SlackResult) types.SlackResult {
	for segmentIndex := range result.Segments {
		segment := &result.Segments[segmentIndex]
		switch segment.Kind {
		case types.SlackSegmentMRKDWN, types.SlackSegmentContext:
			segment.Text = normalizeSlackBold(segment.Text)
		case types.SlackSegmentTable:
			if segment.Table == nil {
				continue
			}
			for rowIndex := range segment.Table.Rows {
				for cellIndex := range segment.Table.Rows[rowIndex] {
					cell := &segment.Table.Rows[rowIndex][cellIndex]
					if cell.Type == "rich_text" {
						cell.Text = normalizeSlackBold(cell.Text)
					}
				}
			}
		}
	}
	return result
}

func normalizeSlackBold(text string) string {
	return mapOutsideCode(text, func(fragment string) string {
		return strings.ReplaceAll(fragment, "**", "*")
	})
}

func containsDoubleAsteriskOutsideCode(text string) bool {
	found := false
	_ = mapOutsideCode(text, func(fragment string) string {
		found = found || strings.Contains(fragment, "**")
		return fragment
	})
	return found
}

func mapOutsideCode(text string, transform func(string) string) string {
	lines := strings.Split(text, "\n")
	inFence := false
	for lineIndex, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		parts := strings.Split(line, "`")
		for partIndex := 0; partIndex < len(parts); partIndex += 2 {
			parts[partIndex] = transform(parts[partIndex])
		}
		lines[lineIndex] = strings.Join(parts, "`")
	}
	return strings.Join(lines, "\n")
}

// promoteMarkdownTables turns a conventional pipe table in model prose into
// the typed table segment rendered by Slack's native Block Kit table block.
// Models occasionally fall back to Markdown even when the structured-output
// contract requests a native table. Promotion keeps presentation deterministic
// without weakening the privileged segment or renderer boundaries. Fenced code
// is intentionally left alone because literal terminal alignment may be the
// content there.
func promoteMarkdownTables(result types.SlackResult) types.SlackResult {
	promoted := make([]types.SlackSegment, 0, len(result.Segments))
	for _, segment := range result.Segments {
		if segment.Kind != types.SlackSegmentMRKDWN || !strings.Contains(segment.Text, "|") {
			promoted = append(promoted, segment)
			continue
		}
		promoted = append(promoted, promoteMarkdownTableSegment(segment)...)
	}
	result.Segments = promoted
	return result
}

func promoteMarkdownTableSegment(segment types.SlackSegment) []types.SlackSegment {
	lines := strings.Split(segment.Text, "\n")
	result := make([]types.SlackSegment, 0, 3)
	textStart := 0
	inFence := false
	flushText := func(end int) {
		if text := strings.TrimSpace(strings.Join(lines[textStart:end], "\n")); text != "" {
			result = append(result, types.SlackSegment{Kind: types.SlackSegmentMRKDWN, Text: text})
		}
	}
	for index := 0; index+1 < len(lines); {
		trimmed := strings.TrimSpace(lines[index])
		if strings.HasPrefix(trimmed, "```") {
			inFence = !inFence
			index++
			continue
		}
		if inFence {
			index++
			continue
		}
		headers, ok := splitMarkdownTableRow(lines[index])
		if !ok || len(headers) < 2 {
			index++
			continue
		}
		alignments, ok := markdownTableAlignments(lines[index+1], len(headers))
		if !ok {
			index++
			continue
		}
		rows := make([][]types.SlackTableCell, 0)
		end := index + 2
		for end < len(lines) {
			cells, rowOK := splitMarkdownTableRow(lines[end])
			if !rowOK || len(cells) != len(headers) {
				break
			}
			row := make([]types.SlackTableCell, len(cells))
			for cellIndex, cell := range cells {
				row[cellIndex] = types.SlackTableCell{Type: "raw_text", Text: stripFormatting(cell)}
			}
			rows = append(rows, row)
			end++
		}
		if len(rows) == 0 {
			index++
			continue
		}
		flushText(index)
		columns := make([]types.SlackTableColumn, len(headers))
		for columnIndex, header := range headers {
			columns[columnIndex] = types.SlackTableColumn{Header: stripFormatting(header), Align: alignments[columnIndex], Wrapped: true}
		}
		result = append(result, types.SlackSegment{Kind: types.SlackSegmentTable, Table: &types.SlackTable{Columns: columns, Rows: rows}})
		textStart = end
		index = end
	}
	flushText(len(lines))
	if len(result) == 0 {
		return []types.SlackSegment{segment}
	}
	return result
}

func splitMarkdownTableRow(line string) ([]string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || !strings.Contains(trimmed, "|") {
		return nil, false
	}
	cells := make([]string, 0, 4)
	var cell strings.Builder
	escaped := false
	for _, character := range trimmed {
		if escaped {
			if character != '|' {
				cell.WriteRune('\\')
			}
			cell.WriteRune(character)
			escaped = false
			continue
		}
		if character == '\\' {
			escaped = true
			continue
		}
		if character == '|' {
			cells = append(cells, strings.TrimSpace(cell.String()))
			cell.Reset()
			continue
		}
		cell.WriteRune(character)
	}
	if escaped {
		cell.WriteRune('\\')
	}
	cells = append(cells, strings.TrimSpace(cell.String()))
	if strings.HasPrefix(trimmed, "|") && len(cells) > 0 && cells[0] == "" {
		cells = cells[1:]
	}
	if strings.HasSuffix(trimmed, "|") && len(cells) > 0 && cells[len(cells)-1] == "" {
		cells = cells[:len(cells)-1]
	}
	return cells, len(cells) >= 2
}

func markdownTableAlignments(line string, columns int) ([]string, bool) {
	cells, ok := splitMarkdownTableRow(line)
	if !ok || len(cells) != columns {
		return nil, false
	}
	alignments := make([]string, columns)
	for index, cell := range cells {
		trimmed := strings.TrimSpace(cell)
		left, right := strings.HasPrefix(trimmed, ":"), strings.HasSuffix(trimmed, ":")
		core := strings.Trim(trimmed, ":")
		if len(core) < 3 || strings.Trim(core, "-") != "" {
			return nil, false
		}
		switch {
		case left && right:
			alignments[index] = "center"
		case right:
			alignments[index] = "right"
		default:
			alignments[index] = "left"
		}
	}
	return alignments, true
}

func modelSegmentsAllowed(segments []types.SlackSegment) bool {
	for _, segment := range segments {
		if segment.Kind == types.SlackSegmentApproval || segment.Approval != nil || segment.Kind == types.SlackSegmentNotice || segment.Notice != nil {
			return false
		}
	}
	return true
}
