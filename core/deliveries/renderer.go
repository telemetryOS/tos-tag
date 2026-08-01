package deliveries

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
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
	appendBlockGroup := func(blocks []map[string]any, fallback string) {
		last := &payloads[len(payloads)-1]
		if len(last.Blocks) > 0 && len(last.Blocks)+len(blocks) > maxBlocksPerMessage {
			payloads = append(payloads, Payload{})
		}
		for blockIndex, block := range blocks {
			blockFallback := ""
			if blockIndex == 0 {
				blockFallback = fallback
			}
			appendBlock(block, blockFallback)
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
		case types.SlackSegmentApproval:
			if segment.Approval == nil || segment.Text != "" || segment.Table != nil || segment.Artifact != nil || segment.Notice != nil {
				return nil, fmt.Errorf("%w: segment %d has invalid approval payload", ErrInvalidResult, index)
			}
			approval := segment.Approval
			status := approval.Status
			if status == "" {
				status = "pending"
			}
			if approval.ID == "" || approval.ActionHash == "" || approval.ToolID == "" || approval.OperationID == "" || approval.Risk == "" || approval.Destination == "" || (status == "pending" && !approval.ExpiresAt.After(time.Now().UTC())) || (status != "pending" && status != "approved" && status != "denied" && status != "expired") || (status != "pending" && approval.ResolvedAt.IsZero()) {
				return nil, fmt.Errorf("%w: segment %d has incomplete approval payload", ErrInvalidResult, index)
			}
			actionJSON, err := json.Marshal(map[string]any{"tool_id": approval.ToolID, "operation_id": approval.OperationID, "arguments": approval.Arguments, "destination": approval.Destination, "risk": approval.Risk, "action_hash": approval.ActionHash})
			if err != nil || len(actionJSON) > 1800 {
				return nil, fmt.Errorf("%w: segment %d approval action is not renderable", ErrInvalidResult, index)
			}
			fallback := fmt.Sprintf("Approval required for %s.%s (%s).", approval.ToolID, approval.OperationID, approval.Risk)
			if status == "approved" {
				fallback = fmt.Sprintf("Action approved: %s.%s (%s).", approval.ToolID, approval.OperationID, approval.Risk)
			} else if status == "denied" {
				fallback = fmt.Sprintf("Action denied: %s.%s (%s).", approval.ToolID, approval.OperationID, approval.Risk)
			} else if status == "expired" {
				fallback = fmt.Sprintf("Approval expired: %s.%s (%s).", approval.ToolID, approval.OperationID, approval.Risk)
			}
			appendBlockGroup(renderApprovalBlocks(approval), fallback)
		case types.SlackSegmentNotice:
			if segment.Notice == nil || segment.Text != "" || segment.Table != nil || segment.Artifact != nil || segment.Approval != nil {
				return nil, fmt.Errorf("%w: segment %d has invalid notice payload", ErrInvalidResult, index)
			}
			if err := validateNotice(*segment.Notice); err != nil {
				return nil, fmt.Errorf("segment %d: %w", index, err)
			}
			appendBlockGroup(renderNoticeBlocks(segment.Notice, index), segment.Notice.Title+": "+stripFormatting(segment.Notice.Message))
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

func renderApprovalBlocks(approval *types.SlackApproval) []map[string]any {
	action := approval.ToolID + "." + approval.OperationID
	status := approval.Status
	if status == "" {
		status = "pending"
	}
	blocks := []map[string]any{
		{
			"type":     "header",
			"block_id": "tos_tag_approval_header/" + approval.ID,
			"text":     map[string]any{"type": "plain_text", "text": titleForApprovalStatus(status), "emoji": true},
		},
		map[string]any{
			"type":     "section",
			"block_id": "tos_tag_approval_summary/" + approval.ID,
			"text":     map[string]any{"type": "mrkdwn", "text": approvalSubtitle(approval, status)},
			"fields": []any{
				map[string]any{"type": "mrkdwn", "text": "*Action*\n`" + approvalInlineCode(action) + "`"},
				map[string]any{"type": "mrkdwn", "text": "*Risk*\n`" + approvalInlineCode(approval.Risk) + "`"},
				map[string]any{"type": "mrkdwn", "text": "*Destination*\n`" + approvalInlineCode(approval.Destination) + "`"},
			},
		},
	}
	blocks = append(blocks, approvalArgumentBlocks(approval)...)
	blocks = append(blocks, map[string]any{
		"type":     "context",
		"block_id": "tos_tag_approval_context/" + approval.ID,
		"elements": []any{
			map[string]any{"type": "plain_text", "text": approvalContext(approval, status)},
		},
	})
	if status == "pending" {
		blocks = append(blocks, map[string]any{
			"type":     "actions",
			"block_id": "tos_tag_approval_actions/" + approval.ID,
			"elements": []any{
				map[string]any{"type": "button", "action_id": "tos_tag_approval_approve", "text": map[string]any{"type": "plain_text", "text": "Approve"}, "style": "primary", "value": approval.ID, "confirm": map[string]any{"title": map[string]any{"type": "plain_text", "text": "Approve action?"}, "text": map[string]any{"type": "mrkdwn", "text": "This authorizes only the exact action summarized in the approval card."}, "confirm": map[string]any{"type": "plain_text", "text": "Approve"}, "deny": map[string]any{"type": "plain_text", "text": "Cancel"}}},
				map[string]any{"type": "button", "action_id": "tos_tag_approval_deny", "text": map[string]any{"type": "plain_text", "text": "Deny"}, "style": "danger", "value": approval.ID},
			},
		})
	}
	return blocks
}

func titleForApprovalStatus(status string) string {
	title := "Approval required"
	if status == "approved" {
		title = "Action approved"
	} else if status == "denied" {
		title = "Action denied"
	} else if status == "expired" {
		title = "Approval expired"
	}
	return title
}

func approvalSubtitle(approval *types.SlackApproval, status string) string {
	if status == "approved" {
		return "A fresh agent worker is resuming from this exact action."
	}
	if status == "denied" {
		return "The suspended request was cancelled."
	}
	if status == "expired" {
		return "The approval window closed and the suspended request was cancelled."
	}
	return "Review this " + strings.ToLower(humanizeApprovalName(approval.Risk)) + " action before it runs."
}

func approvalContext(approval *types.SlackApproval, status string) string {
	label := "Expires " + approval.ExpiresAt.UTC().Format(time.RFC3339)
	if status == "approved" {
		label = "Approved " + approval.ResolvedAt.UTC().Format(time.RFC3339)
	} else if status == "denied" {
		label = "Denied " + approval.ResolvedAt.UTC().Format(time.RFC3339)
	} else if status == "expired" {
		label = "Expired " + approval.ExpiresAt.UTC().Format(time.RFC3339)
	}
	return label + "  •  Exact-action fingerprint " + approvalFingerprint(approval.ActionHash)
}

func validateNotice(notice types.SlackNotice) error {
	switch notice.Tone {
	case "info", "success", "warning", "error":
	default:
		return fmt.Errorf("%w: invalid notice tone", ErrInvalidResult)
	}
	icon := map[string]string{"info": "ℹ️", "success": "✅", "warning": "⚠️", "error": "⚠️"}[notice.Tone]
	if strings.TrimSpace(notice.Title) == "" || utf8.RuneCountInString(icon+" "+notice.Title) > 150 || strings.TrimSpace(notice.Message) == "" || utf8.RuneCountInString(notice.Message) > 200 || utf8.RuneCountInString(notice.Context) > 200 {
		return fmt.Errorf("%w: invalid notice content", ErrInvalidResult)
	}
	if err := validateMRKDWN(notice.Message); err != nil {
		return err
	}
	return nil
}

func renderNoticeBlocks(notice *types.SlackNotice, index int) []map[string]any {
	icon := map[string]string{"info": "ℹ️", "success": "✅", "warning": "⚠️", "error": "⚠️"}[notice.Tone]
	blocks := []map[string]any{
		{
			"type":     "header",
			"block_id": fmt.Sprintf("tos_tag_notice_header/%d", index),
			"text":     map[string]any{"type": "plain_text", "text": icon + " " + notice.Title, "emoji": true},
		},
		{
			"type":     "section",
			"block_id": fmt.Sprintf("tos_tag_notice_message/%d", index),
			"text":     map[string]any{"type": "mrkdwn", "text": notice.Message},
		},
	}
	if notice.Context != "" {
		blocks = append(blocks, map[string]any{
			"type":     "context",
			"block_id": fmt.Sprintf("tos_tag_notice_context/%d", index),
			"elements": []any{map[string]any{"type": "plain_text", "text": notice.Context}},
		})
	}
	return blocks
}

func approvalArgumentBlocks(approval *types.SlackApproval) []map[string]any {
	keys := make([]string, 0, len(approval.Arguments))
	for key := range approval.Arguments {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	fields := make([]any, 0, len(keys))
	for _, key := range keys {
		value, _ := approvalValue(approval.Arguments[key])
		if value == "" {
			value = "(empty)"
		}
		fields = append(fields, map[string]any{
			"type":  "plain_text",
			"text":  humanizeApprovalName(key) + "\n" + value,
			"emoji": true,
		})
	}
	if len(fields) == 0 {
		return []map[string]any{{
			"type":     "section",
			"block_id": "tos_tag_approval_arguments/" + approval.ID + "/0",
			"text":     map[string]any{"type": "mrkdwn", "text": "*Requested changes*\nNo arguments"},
		}}
	}
	blocks := make([]map[string]any, 0, (len(fields)+9)/10)
	for start := 0; start < len(fields); start += 10 {
		end := min(start+10, len(fields))
		blocks = append(blocks, map[string]any{
			"type":     "section",
			"block_id": fmt.Sprintf("tos_tag_approval_arguments/%s/%d", approval.ID, start/10),
			"text":     map[string]any{"type": "mrkdwn", "text": "*Requested changes*"},
			"fields":   fields[start:end],
		})
	}
	return blocks
}

func approvalValue(value any) (string, bool) {
	if text, ok := value.(string); ok {
		text = strings.ToValidUTF8(text, "�")
		return text, len(text) <= 80 && !strings.ContainsAny(text, " \t\r\n")
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return "unrenderable value", false
	}
	return string(encoded), true
}

func approvalInlineCode(value string) string {
	value = strings.ToValidUTF8(value, "�")
	value = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", "`", "'", "\r", " ", "\n", " ").Replace(value)
	return strings.TrimSpace(value)
}

func approvalFingerprint(value string) string {
	const visible = 19
	if len(value) <= visible {
		return value
	}
	return value[:visible] + "…"
}

func humanizeApprovalName(value string) string {
	words := strings.FieldsFunc(strings.TrimSpace(value), func(character rune) bool {
		return character == '_' || character == '-' || character == '.'
	})
	if len(words) == 0 {
		return "Unknown"
	}
	runes := []rune(strings.ToLower(strings.Join(words, " ")))
	runes[0] = []rune(strings.ToUpper(string(runes[0])))[0]
	return string(runes)
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
		if chunk := strings.TrimSpace(string(runes[:cut])); chunk != "" {
			chunks = append(chunks, chunk)
		}
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
