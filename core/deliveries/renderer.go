package deliveries

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/telemetryos/tos-tag/types"
)

const (
	maxBlocksPerMessage  = 50
	maxSectionCharacters = 3000
	maxTableRows         = 100
	maxTableColumns      = 20
	maxTableCharacters   = 10_000
)

var (
	ErrInvalidResult   = errors.New("invalid Slack result")
	gfmLinkPattern     = regexp.MustCompile(`\[[^\]\n]+\]\([^)\n]+\)`)
	slackLinkPattern   = regexp.MustCompile(`<([^>|]+)\|([^>]+)>`)
	slackEntityPattern = regexp.MustCompile(`<(?:@|#|!)[^>]*>`)
)

type Payload struct {
	Text   string           `json:"text"`
	Blocks []map[string]any `json:"blocks"`
}

type Renderer struct{}

func NewRenderer() *Renderer { return &Renderer{} }

func (r *Renderer) Render(result types.SlackResult) ([]Payload, error) {
	if len(result.Segments) == 0 {
		return nil, fmt.Errorf("%w: no segments", ErrInvalidResult)
	}
	payloads := []Payload{{}}
	appendBlock := func(block map[string]any, fallback string) {
		last := &payloads[len(payloads)-1]
		if len(last.Blocks) >= maxBlocksPerMessage {
			payloads = append(payloads, Payload{})
			last = &payloads[len(payloads)-1]
		}
		last.Blocks = append(last.Blocks, block)
		if fallback != "" {
			if last.Text != "" {
				last.Text += "\n"
			}
			last.Text += fallback
		}
	}

	for index, segment := range result.Segments {
		switch segment.Kind {
		case types.SlackSegmentMRKDWN:
			if segment.Table != nil || segment.Artifact != nil {
				return nil, fmt.Errorf("%w: segment %d mixes mrkdwn with another payload", ErrInvalidResult, index)
			}
			if err := validateMRKDWN(segment.Text); err != nil {
				return nil, fmt.Errorf("segment %d: %w", index, err)
			}
			chunks, err := splitMRKDWN(segment.Text, maxSectionCharacters)
			if err != nil {
				return nil, fmt.Errorf("segment %d: %w", index, err)
			}
			for _, chunk := range chunks {
				appendBlock(map[string]any{
					"type": "section",
					"text": map[string]any{"type": "mrkdwn", "text": chunk},
				}, stripFormatting(chunk))
			}
		case types.SlackSegmentTable:
			if segment.Table == nil || segment.Text != "" || segment.Artifact != nil {
				return nil, fmt.Errorf("%w: segment %d has invalid table payload", ErrInvalidResult, index)
			}
			tables, err := renderTables(*segment.Table)
			if err != nil {
				return nil, fmt.Errorf("segment %d: %w", index, err)
			}
			for tableIndex, table := range tables {
				last := &payloads[len(payloads)-1]
				if containsTable(last.Blocks) || len(last.Blocks) >= maxBlocksPerMessage {
					payloads = append(payloads, Payload{})
				}
				appendBlock(table, tableFallback(*segment.Table, tableIndex, len(tables)))
			}
		case types.SlackSegmentArtifact:
			if segment.Artifact == nil || segment.Text != "" || segment.Table != nil {
				return nil, fmt.Errorf("%w: segment %d has invalid artifact payload", ErrInvalidResult, index)
			}
			if err := validateArtifact(*segment.Artifact); err != nil {
				return nil, fmt.Errorf("segment %d: %w", index, err)
			}
			text := fmt.Sprintf("<%s|%s>", segment.Artifact.URL, escapeLabel(segment.Artifact.Name))
			appendBlock(map[string]any{
				"type": "section",
				"text": map[string]any{"type": "mrkdwn", "text": text},
			}, segment.Artifact.Name+": "+segment.Artifact.URL)
		default:
			return nil, fmt.Errorf("%w: segment %d has unknown kind %q", ErrInvalidResult, index, segment.Kind)
		}
	}
	for _, payload := range payloads {
		if len(payload.Blocks) == 0 {
			return nil, fmt.Errorf("%w: empty rendered payload", ErrInvalidResult)
		}
	}
	return payloads, nil
}

func validateMRKDWN(text string) error {
	if strings.TrimSpace(text) == "" {
		return fmt.Errorf("%w: empty mrkdwn", ErrInvalidResult)
	}
	if !utf8.ValidString(text) || strings.ContainsRune(text, '\x00') {
		return fmt.Errorf("%w: invalid text encoding", ErrInvalidResult)
	}
	if gfmLinkPattern.MatchString(text) {
		return fmt.Errorf("%w: GitHub link syntax is not allowed", ErrInvalidResult)
	}
	if strings.Contains(text, "**") {
		return fmt.Errorf("%w: double-asterisk bold is not Slack mrkdwn", ErrInvalidResult)
	}
	if slackEntityPattern.MatchString(text) {
		return fmt.Errorf("%w: Slack user, channel, group, and special mentions are not allowed", ErrInvalidResult)
	}
	if strings.Count(text, "```")%2 != 0 {
		return fmt.Errorf("%w: unbalanced fenced code block", ErrInvalidResult)
	}
	for _, match := range slackLinkPattern.FindAllStringSubmatch(text, -1) {
		parsed, err := url.Parse(match[1])
		if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
			return fmt.Errorf("%w: unsafe Slack link", ErrInvalidResult)
		}
	}
	return nil
}

func splitMRKDWN(text string, limit int) ([]string, error) {
	if utf8.RuneCountInString(text) <= limit {
		return []string{text}, nil
	}
	if strings.Contains(text, "```") {
		return nil, fmt.Errorf("%w: oversized fenced code requires artifact fallback", ErrInvalidResult)
	}
	var chunks []string
	remaining := text
	for utf8.RuneCountInString(remaining) > limit {
		runes := []rune(remaining)
		cut := limit
		prefix := string(runes[:limit])
		if newline := strings.LastIndex(prefix, "\n"); newline > limit/2 {
			cut = utf8.RuneCountInString(prefix[:newline])
		}
		chunks = append(chunks, strings.TrimSpace(string(runes[:cut])))
		remaining = strings.TrimSpace(string(runes[cut:]))
	}
	if remaining != "" {
		chunks = append(chunks, remaining)
	}
	return chunks, nil
}

func renderTables(table types.SlackTable) ([]map[string]any, error) {
	if len(table.Columns) == 0 || len(table.Columns) > maxTableColumns {
		return nil, fmt.Errorf("%w: table must have 1-%d columns", ErrInvalidResult, maxTableColumns)
	}
	for rowIndex, row := range table.Rows {
		if len(row) != len(table.Columns) {
			return nil, fmt.Errorf("%w: row %d has %d cells for %d columns", ErrInvalidResult, rowIndex, len(row), len(table.Columns))
		}
	}

	var rendered []map[string]any
	for start := 0; start < len(table.Rows) || (start == 0 && len(table.Rows) == 0); {
		rows := []any{renderHeader(table.Columns)}
		characters := tableHeaderCharacters(table.Columns)
		end := start
		for end < len(table.Rows) && len(rows) < maxTableRows {
			rowChars := tableRowCharacters(table.Rows[end])
			if rowChars > maxTableCharacters {
				return nil, fmt.Errorf("%w: row %d exceeds table character limit", ErrInvalidResult, end)
			}
			if characters+rowChars > maxTableCharacters {
				break
			}
			row, err := renderRow(table.Rows[end])
			if err != nil {
				return nil, fmt.Errorf("row %d: %w", end, err)
			}
			rows = append(rows, row)
			characters += rowChars
			end++
		}
		if end == start && len(table.Rows) > 0 {
			return nil, fmt.Errorf("%w: table header and row exceed character limit", ErrInvalidResult)
		}
		columns := make([]any, len(table.Columns))
		for i, column := range table.Columns {
			setting := map[string]any{"is_wrapped": column.Wrapped}
			switch column.Align {
			case "", "left", "center", "right":
				if column.Align != "" {
					setting["align"] = column.Align
				}
			default:
				return nil, fmt.Errorf("%w: invalid alignment %q", ErrInvalidResult, column.Align)
			}
			columns[i] = setting
		}
		rendered = append(rendered, map[string]any{"type": "table", "column_settings": columns, "rows": rows})
		if len(table.Rows) == 0 {
			break
		}
		start = end
	}
	return rendered, nil
}

func renderHeader(columns []types.SlackTableColumn) []any {
	row := make([]any, len(columns))
	for i, column := range columns {
		row[i] = map[string]any{
			"type": "rich_text",
			"elements": []any{map[string]any{
				"type":     "rich_text_section",
				"elements": []any{map[string]any{"type": "text", "text": column.Header, "style": map[string]any{"bold": true}}},
			}},
		}
	}
	return row
}

func renderRow(cells []types.SlackTableCell) ([]any, error) {
	row := make([]any, len(cells))
	for i, cell := range cells {
		switch cell.Type {
		case "raw_text", "":
			row[i] = map[string]any{"type": "raw_text", "text": cell.Text}
		case "raw_number":
			row[i] = map[string]any{"type": "raw_number", "value": cell.Number}
		case "rich_text":
			if err := validateMRKDWN(cell.Text); err != nil {
				return nil, err
			}
			row[i] = map[string]any{
				"type": "rich_text",
				"elements": []any{map[string]any{
					"type":     "rich_text_section",
					"elements": []any{map[string]any{"type": "text", "text": cell.Text}},
				}},
			}
		default:
			return nil, fmt.Errorf("%w: invalid table cell type %q", ErrInvalidResult, cell.Type)
		}
	}
	return row, nil
}

func containsTable(blocks []map[string]any) bool {
	for _, block := range blocks {
		if block["type"] == "table" {
			return true
		}
	}
	return false
}

func tableHeaderCharacters(columns []types.SlackTableColumn) int {
	total := 0
	for _, column := range columns {
		total += utf8.RuneCountInString(column.Header)
	}
	return total
}

func tableRowCharacters(row []types.SlackTableCell) int {
	total := 0
	for _, cell := range row {
		if cell.Type == "raw_number" {
			total += len(fmt.Sprint(cell.Number))
		} else {
			total += utf8.RuneCountInString(cell.Text)
		}
	}
	return total
}

func tableFallback(table types.SlackTable, part, parts int) string {
	label := "Table"
	if len(table.Columns) > 0 {
		headers := make([]string, len(table.Columns))
		for i, column := range table.Columns {
			headers[i] = column.Header
		}
		label += ": " + strings.Join(headers, ", ")
	}
	if parts > 1 {
		label += fmt.Sprintf(" (part %d of %d)", part+1, parts)
	}
	return label
}

func validateArtifact(artifact types.SlackArtifact) error {
	if strings.TrimSpace(artifact.Name) == "" {
		return fmt.Errorf("%w: artifact name is required", ErrInvalidResult)
	}
	parsed, err := url.Parse(artifact.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("%w: artifact URL must be HTTPS", ErrInvalidResult)
	}
	return nil
}

func escapeLabel(label string) string {
	replacer := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "|", "-")
	return replacer.Replace(label)
}

func stripFormatting(text string) string {
	text = slackLinkPattern.ReplaceAllString(text, "$2 ($1)")
	text = strings.NewReplacer("*", "", "_", "", "~", "", "`", "").Replace(text)
	return text
}
