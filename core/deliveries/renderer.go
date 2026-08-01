package deliveries

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/telemetryos/tos-tag/types"
)

const (
	maxBlocksPerMessage  = 50
	maxHeaderCharacters  = 150
	maxSectionCharacters = 3000
	maxContextCharacters = 2000
	maxImageCharacters   = 2000
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

// ValidationCode returns a stable, content-free diagnostic for a renderer
// rejection. The code is safe to retain in logs and job state; it never embeds
// model output, Slack content, or table cell values.
func ValidationCode(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	checks := []struct {
		fragment string
		code     string
	}{
		{"no segments", "no_segments"},
		{"invalid header payload", "header_payload"},
		{"mixes mrkdwn", "mrkdwn_mixed_payload"},
		{"invalid context payload", "context_payload"},
		{"invalid divider payload", "divider_payload"},
		{"invalid table payload", "table_payload"},
		{"invalid image payload", "image_payload"},
		{"invalid artifact payload", "artifact_payload"},
		{"invalid approval payload", "approval_payload"},
		{"incomplete approval payload", "approval_incomplete"},
		{"approval action is not renderable", "approval_action_size"},
		{"invalid notice payload", "notice_payload"},
		{"unknown kind", "unknown_segment_kind"},
		{"lacks accessible fallback text", "missing_fallback"},
		{"GitHub link syntax", "mrkdwn_github_link"},
		{"double-asterisk bold", "mrkdwn_double_asterisk"},
		{"Slack mention lacks admitted source provenance", "mrkdwn_forbidden_mention"},
		{"unbalanced fenced code block", "mrkdwn_unbalanced_code"},
		{"unsafe Slack link", "mrkdwn_unsafe_link"},
		{"oversized fenced code", "mrkdwn_oversized_code"},
		{"table must have", "table_columns"},
		{"cells for", "table_row_shape"},
		{"exceeds table character limit", "table_row_size"},
		{"table header and row exceed", "table_total_size"},
		{"invalid alignment", "table_alignment"},
		{"invalid table cell type", "table_cell_type"},
		{"image URL must be HTTPS", "image_url"},
		{"image alt text is invalid", "image_alt_text"},
		{"image title is invalid", "image_title"},
		{"artifact name is required", "artifact_name"},
		{"artifact URL must be HTTPS", "artifact_url"},
		{"artifact URL lacks successful tool provenance", "artifact_unverified"},
	}
	for _, check := range checks {
		if strings.Contains(message, check.fragment) {
			return check.code
		}
	}
	return "invalid_result"
}

// ValidateArtifactProvenance ensures a model-created artifact segment refers to
// a URL produced by a successful reviewed tool call in the same disposable
// worker attempt. Existing/reference links belong in mrkdwn; the artifact
// segment represents a newly published result and must not be hallucinated.
func ValidateArtifactProvenance(result types.SlackResult, producedURLs map[string]struct{}) error {
	for index, segment := range result.Segments {
		if segment.Kind != types.SlackSegmentArtifact || segment.Artifact == nil {
			continue
		}
		if _, ok := producedURLs[segment.Artifact.URL]; !ok {
			return fmt.Errorf("%w: segment %d artifact URL lacks successful tool provenance", ErrInvalidResult, index)
		}
	}
	return nil
}

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
		if len(last.Blocks) > 0 && (len(last.Blocks)+len(blocks) > maxBlocksPerMessage || (containsTable(blocks) && containsTable(last.Blocks))) {
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
		case types.SlackSegmentHeader:
			if segment.Table != nil || segment.Image != nil || segment.Artifact != nil || segment.Approval != nil || segment.Notice != nil || strings.TrimSpace(segment.Text) == "" || !utf8.ValidString(segment.Text) || strings.ContainsRune(segment.Text, '\x00') || utf8.RuneCountInString(segment.Text) > maxHeaderCharacters {
				return nil, fmt.Errorf("%w: segment %d has invalid header payload", ErrInvalidResult, index)
			}
			appendBlock(map[string]any{
				"type": "header",
				"text": map[string]any{"type": "plain_text", "text": segment.Text, "emoji": true},
			}, segment.Text)
		case types.SlackSegmentMRKDWN:
			if segment.Table != nil || segment.Image != nil || segment.Artifact != nil || segment.Approval != nil || segment.Notice != nil {
				return nil, fmt.Errorf("%w: segment %d mixes mrkdwn with another payload", ErrInvalidResult, index)
			}
			if err := validateMRKDWN(segment.Text, result.AllowedMentions); err != nil {
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
		case types.SlackSegmentContext:
			if segment.Table != nil || segment.Image != nil || segment.Artifact != nil || segment.Approval != nil || segment.Notice != nil || utf8.RuneCountInString(segment.Text) > maxContextCharacters {
				return nil, fmt.Errorf("%w: segment %d has invalid context payload", ErrInvalidResult, index)
			}
			if err := validateMRKDWN(segment.Text, result.AllowedMentions); err != nil {
				return nil, fmt.Errorf("segment %d: %w", index, err)
			}
			appendBlock(map[string]any{
				"type":     "context",
				"elements": []any{map[string]any{"type": "mrkdwn", "text": segment.Text}},
			}, stripFormatting(segment.Text))
		case types.SlackSegmentDivider:
			if segment.Text != "" || segment.Table != nil || segment.Image != nil || segment.Artifact != nil || segment.Approval != nil || segment.Notice != nil {
				return nil, fmt.Errorf("%w: segment %d has invalid divider payload", ErrInvalidResult, index)
			}
			appendBlock(map[string]any{"type": "divider"}, "")
		case types.SlackSegmentTable:
			if segment.Table == nil || segment.Text != "" || segment.Image != nil || segment.Artifact != nil || segment.Approval != nil || segment.Notice != nil {
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
		case types.SlackSegmentImage:
			if segment.Image == nil || segment.Text != "" || segment.Table != nil || segment.Artifact != nil || segment.Approval != nil || segment.Notice != nil {
				return nil, fmt.Errorf("%w: segment %d has invalid image payload", ErrInvalidResult, index)
			}
			if err := validateImage(*segment.Image); err != nil {
				return nil, fmt.Errorf("segment %d: %w", index, err)
			}
			block := map[string]any{"type": "image", "image_url": segment.Image.URL, "alt_text": segment.Image.AltText}
			if segment.Image.Title != "" {
				block["title"] = map[string]any{"type": "plain_text", "text": segment.Image.Title, "emoji": true}
			}
			appendBlock(block, "Image: "+segment.Image.AltText+" ("+segment.Image.URL+")")
		case types.SlackSegmentArtifact:
			if segment.Artifact == nil || segment.Text != "" || segment.Table != nil || segment.Image != nil || segment.Approval != nil || segment.Notice != nil {
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
			if segment.Approval == nil || segment.Text != "" || segment.Table != nil || segment.Image != nil || segment.Artifact != nil || segment.Notice != nil {
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
			actionJSON, err := json.Marshal(map[string]any{"tool_id": approval.ToolID, "operation_id": approval.OperationID, "arguments": approvalDisplayArguments(approval), "destination": approval.Destination, "risk": approval.Risk, "action_hash": approval.ActionHash})
			if err != nil || len(actionJSON) > 1800 {
				return nil, fmt.Errorf("%w: segment %d approval action is not renderable", ErrInvalidResult, index)
			}
			blocks, err := renderApprovalBlocks(approval)
			if err != nil {
				return nil, fmt.Errorf("segment %d: %w", index, err)
			}
			appendBlockGroup(blocks, approvalFallback(approval, status))
		case types.SlackSegmentNotice:
			if segment.Notice == nil || segment.Text != "" || segment.Table != nil || segment.Image != nil || segment.Artifact != nil || segment.Approval != nil {
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
		if len(payload.Blocks) == 0 || strings.TrimSpace(payload.Text) == "" {
			return nil, fmt.Errorf("%w: rendered payload lacks accessible fallback text", ErrInvalidResult)
		}
	}
	return payloads, nil
}

func renderApprovalBlocks(approval *types.SlackApproval) ([]map[string]any, error) {
	status := approval.Status
	if status == "" {
		status = "pending"
	}
	tables, err := renderTables(approvalTable(approval))
	if err != nil || len(tables) != 1 {
		return nil, fmt.Errorf("%w: approval details do not fit one native table", ErrInvalidResult)
	}
	blocks := []map[string]any{
		{
			"type":     "header",
			"block_id": "tos_tag_approval_header/" + approval.ID,
			"text":     map[string]any{"type": "plain_text", "text": titleForApprovalStatus(status), "emoji": true},
		},
		{
			"type":     "section",
			"block_id": "tos_tag_approval_summary/" + approval.ID,
			"text":     map[string]any{"type": "mrkdwn", "text": approvalSubtitle(approval, status)},
		},
		tables[0],
	}
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
	return blocks, nil
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
	if err := validateMRKDWN(notice.Message, types.SlackMentionAllowlist{}); err != nil {
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

func approvalTable(approval *types.SlackApproval) types.SlackTable {
	arguments := approvalDisplayArguments(approval)
	keys := make([]string, 0, len(arguments))
	for key := range arguments {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rows := [][]types.SlackTableCell{
		{{Type: "raw_text", Text: "Action"}, {Type: "raw_text", Text: approval.ToolID + "." + approval.OperationID}},
		{{Type: "raw_text", Text: "Risk"}, {Type: "raw_text", Text: approval.Risk}},
		{{Type: "raw_text", Text: "Destination"}, {Type: "raw_text", Text: approval.Destination}},
	}
	for _, key := range keys {
		value, _ := approvalValue(arguments[key])
		if value == "" {
			value = "(empty)"
		}
		rows = append(rows, []types.SlackTableCell{{Type: "raw_text", Text: humanizeApprovalName(key)}, {Type: "raw_text", Text: value}})
	}
	if len(keys) == 0 {
		rows = append(rows, []types.SlackTableCell{{Type: "raw_text", Text: "Requested changes"}, {Type: "raw_text", Text: "None"}})
	}
	return types.SlackTable{
		Columns: []types.SlackTableColumn{
			{Header: "Field", Wrapped: true},
			{Header: "Value", Wrapped: true},
		},
		Rows: rows,
	}
}

func approvalFallback(approval *types.SlackApproval, status string) string {
	label := "Approval required"
	if status == "approved" {
		label = "Action approved"
	} else if status == "denied" {
		label = "Action denied"
	} else if status == "expired" {
		label = "Approval expired"
	}
	parts := []string{
		fmt.Sprintf("%s: %s.%s (%s)", label, approval.ToolID, approval.OperationID, approval.Risk),
		"destination=" + approval.Destination,
	}
	argumentsMap := approvalDisplayArguments(approval)
	keys := make([]string, 0, len(argumentsMap))
	for key := range argumentsMap {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	if len(keys) > 0 {
		arguments := make([]string, 0, len(keys))
		for _, key := range keys {
			value, _ := approvalValue(argumentsMap[key])
			if value == "" {
				value = "(empty)"
			}
			arguments = append(arguments, key+"="+value)
		}
		parts = append(parts, "requested changes: "+strings.Join(arguments, ", "))
	}
	return strings.Join(parts, "; ") + "."
}

// approvalDisplayArguments keeps the immutable approval action intact while
// preventing large inline document bodies from overwhelming the Slack card.
// The exact action hash still binds the complete body stored by the approval
// repository; only the human-readable projection is summarized here.
func approvalDisplayArguments(approval *types.SlackApproval) map[string]any {
	result := make(map[string]any, len(approval.Arguments))
	for key, value := range approval.Arguments {
		result[key] = value
	}
	if approval.ToolID != "telemetryos.wiki" || approval.OperationID != "write" {
		return result
	}
	value, exists := result["argv"]
	if !exists {
		return result
	}
	argv, ok := stringArguments(value)
	if !ok {
		return result
	}
	for index := 0; index < len(argv); index++ {
		switch {
		case argv[index] == "--body" && index+1 < len(argv):
			argv[index+1] = inlineBodySummary(argv[index+1])
			index++
		case strings.HasPrefix(argv[index], "--body="):
			argv[index] = "--body=" + inlineBodySummary(strings.TrimPrefix(argv[index], "--body="))
		}
	}
	result["argv"] = argv
	return result
}

func stringArguments(value any) ([]string, bool) {
	switch typed := value.(type) {
	case []string:
		return append([]string(nil), typed...), true
	}
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() || (reflected.Kind() != reflect.Slice && reflected.Kind() != reflect.Array) {
		return nil, false
	}
	result := make([]string, reflected.Len())
	for index := 0; index < reflected.Len(); index++ {
		text, ok := reflected.Index(index).Interface().(string)
		if !ok {
			return nil, false
		}
		result[index] = text
	}
	return result, true
}

func inlineBodySummary(body string) string {
	digest := sha256.Sum256([]byte(body))
	return fmt.Sprintf("<inline body: %d bytes; sha256:%x>", len([]byte(body)), digest[:8])
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

func validateMRKDWN(text string, allowedMentions types.SlackMentionAllowlist) error {
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
	for _, entity := range slackEntityPattern.FindAllString(text, -1) {
		if !allowedSlackEntity(entity, allowedMentions) {
			return fmt.Errorf("%w: Slack mention lacks admitted source provenance", ErrInvalidResult)
		}
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

func allowedSlackEntity(entity string, allowed types.SlackMentionAllowlist) bool {
	for _, id := range allowed.UserIDs {
		if id != "" && entity == "<@"+id+">" {
			return true
		}
	}
	for _, id := range allowed.ChannelIDs {
		if id != "" && entity == "<#"+id+">" {
			return true
		}
	}
	return false
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
			display := cell.Text
			if display == "" {
				display = strconv.FormatFloat(cell.Number, 'f', -1, 64)
			}
			row[i] = map[string]any{"type": "raw_number", "value": cell.Number, "text": display}
		case "rich_text":
			if err := validateMRKDWN(cell.Text, types.SlackMentionAllowlist{}); err != nil {
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

func validateImage(image types.SlackImage) error {
	parsed, err := url.Parse(image.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return fmt.Errorf("%w: image URL must be HTTPS", ErrInvalidResult)
	}
	if strings.TrimSpace(image.AltText) == "" || !utf8.ValidString(image.AltText) || strings.ContainsRune(image.AltText, '\x00') || utf8.RuneCountInString(image.AltText) > maxImageCharacters {
		return fmt.Errorf("%w: image alt text is invalid", ErrInvalidResult)
	}
	if !utf8.ValidString(image.Title) || strings.ContainsRune(image.Title, '\x00') || utf8.RuneCountInString(image.Title) > maxImageCharacters {
		return fmt.Errorf("%w: image title is invalid", ErrInvalidResult)
	}
	return nil
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
