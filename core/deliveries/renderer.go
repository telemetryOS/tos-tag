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
	"unicode"
	"unicode/utf8"

	"github.com/telemetryos/tos-tag/types"
)

const (
	maxBlocksPerMessage    = 50
	maxHeaderCharacters    = 150
	maxSectionCharacters   = 3000
	maxContextCharacters   = 2000
	maxImageCharacters     = 2000
	maxTableRows           = 100
	maxTableColumns        = 20
	maxTableCharacters     = 10_000
	maxCardTextCharacters  = 200
	maxCardTitleCharacters = 150
	maxCarouselCards       = 10
	maxArtifactSynopsis    = 600
)

var (
	ErrInvalidResult     = errors.New("invalid Slack result")
	gfmLinkPattern       = regexp.MustCompile(`\[[^\]\n]+\]\([^)\n]+\)`)
	slackLinkPattern     = regexp.MustCompile(`<([^>|]+)\|([^>]+)>`)
	slackEntityPattern   = regexp.MustCompile(`<(?:@|#|!)[^>]*>`)
	httpsLinkPattern     = regexp.MustCompile(`(?i)https://[^\s<>()]+`)
	bareWikiSlugPattern  = regexp.MustCompile(`(?i)(?:^|[\s(])` + "`?" + `(?:primer|artifacts)/[a-z0-9][a-z0-9._/-]*` + "`?" + `(?:$|[\s),.;:])`)
	generalSlugPattern   = regexp.MustCompile(`(?i)(?:^|[\s(])` + "`?" + `[a-z][a-z0-9-]{1,63}/[a-z0-9][a-z0-9._/-]*` + "`?" + `(?:$|[\s),.;:])`)
	wikiSlugTokenPattern = regexp.MustCompile("(?i)`?[a-z][a-z0-9-]{1,63}/[a-z0-9](?:[a-z0-9._/-]*[a-z0-9])?`?")
	wikiReferenceCue     = regexp.MustCompile(`(?i)\b(?:agent wiki|wiki page|wiki reference|wiki source)\b`)
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
		{"invalid card payload", "card_payload"},
		{"invalid carousel payload", "carousel_payload"},
		{"card title is invalid", "card_title"},
		{"card body is invalid", "card_body"},
		{"card subtitle is invalid", "card_subtitle"},
		{"card subtext is invalid", "card_subtext"},
		{"carousel must contain", "carousel_cards"},
		{"card image URL must be HTTPS", "card_image_url"},
		{"card image alt text is invalid", "card_image_alt_text"},
		{"data table caption is invalid", "data_table_caption"},
		{"data table page size", "data_table_page_size"},
		{"data table row header", "data_table_row_header"},
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
		{"bare Wiki page slug", "wiki_reference_slug"},
	}
	for _, check := range checks {
		if strings.Contains(message, check.fragment) {
			return check.code
		}
	}
	return "invalid_result"
}

// ValidateWikiReferenceLinks prevents internal Wiki identifiers from being
// presented as citations. Namespace/slug values are useful to the reviewed
// CLI, but Slack readers need the opaque human HTTPS URL returned by that CLI.
// Existing Wiki pages belong in normal mrkdwn links rather than artifact
// segments, whose provenance contract is reserved for newly published output.
func ValidateWikiReferenceLinks(result types.SlackResult) error {
	validate := func(text string) error {
		withoutLinks := slackLinkPattern.ReplaceAllString(text, " ")
		withoutLinks = httpsLinkPattern.ReplaceAllString(withoutLinks, " ")
		if bareWikiSlugPattern.MatchString(withoutLinks) || (wikiReferenceCue.MatchString(withoutLinks) && generalSlugPattern.MatchString(withoutLinks)) {
			return fmt.Errorf("%w: bare Wiki page slug must be replaced with an HTTPS link", ErrInvalidResult)
		}
		return nil
	}
	for _, segment := range result.Segments {
		if err := validate(segment.Text); err != nil {
			return err
		}
		if segment.Card != nil {
			for _, text := range []string{segment.Card.Title, segment.Card.Subtitle, segment.Card.Body, segment.Card.Subtext} {
				if err := validate(text); err != nil {
					return err
				}
			}
		}
		if segment.Carousel != nil {
			for _, card := range segment.Carousel.Cards {
				for _, text := range []string{card.Title, card.Subtitle, card.Body, card.Subtext} {
					if err := validate(text); err != nil {
						return err
					}
				}
			}
		}
		if segment.Table == nil {
			continue
		}
		for _, row := range segment.Table.Rows {
			for _, cell := range row {
				if err := validate(cell.Text); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// ResolveWikiReferenceLinks replaces a model-emitted namespace/slug only when
// the same worker attempt returned the canonical human URL for that exact
// reference. The map key is the lowercase SHA-256 fingerprint of the tool
// reference, so the control plane does not need to expose page slugs in events
// or logs. Unresolved slugs remain untouched and are rejected by validation.
func ResolveWikiReferenceLinks(result types.SlackResult, resolvedURLs map[string]string) types.SlackResult {
	if len(resolvedURLs) == 0 {
		return result
	}
	result.Segments = append([]types.SlackSegment(nil), result.Segments...)
	for index := range result.Segments {
		if result.Segments[index].Table != nil {
			table := *result.Segments[index].Table
			table.Rows = make([][]types.SlackTableCell, len(result.Segments[index].Table.Rows))
			for rowIndex := range result.Segments[index].Table.Rows {
				table.Rows[rowIndex] = append([]types.SlackTableCell(nil), result.Segments[index].Table.Rows[rowIndex]...)
			}
			result.Segments[index].Table = &table
		}
		if result.Segments[index].Card != nil {
			card := *result.Segments[index].Card
			resolveCardWikiReferences(&card, resolvedURLs)
			result.Segments[index].Card = &card
		}
		if result.Segments[index].Carousel != nil {
			carousel := *result.Segments[index].Carousel
			carousel.Cards = append([]types.SlackCard(nil), carousel.Cards...)
			for cardIndex := range carousel.Cards {
				resolveCardWikiReferences(&carousel.Cards[cardIndex], resolvedURLs)
			}
			result.Segments[index].Carousel = &carousel
		}
		result.Segments[index].Text = resolveWikiReferenceText(result.Segments[index].Text, resolvedURLs, true)
		if result.Segments[index].Table == nil {
			continue
		}
		for rowIndex := range result.Segments[index].Table.Rows {
			for cellIndex := range result.Segments[index].Table.Rows[rowIndex] {
				cell := &result.Segments[index].Table.Rows[rowIndex][cellIndex]
				cell.Text = resolveWikiReferenceText(cell.Text, resolvedURLs, false)
			}
		}
	}
	return result
}

func resolveCardWikiReferences(card *types.SlackCard, resolvedURLs map[string]string) {
	card.Title = resolveWikiReferenceText(card.Title, resolvedURLs, true)
	card.Subtitle = resolveWikiReferenceText(card.Subtitle, resolvedURLs, true)
	card.Body = resolveWikiReferenceText(card.Body, resolvedURLs, true)
	card.Subtext = resolveWikiReferenceText(card.Subtext, resolvedURLs, true)
}

func resolveWikiReferenceText(text string, resolvedURLs map[string]string, slackLink bool) string {
	matches := wikiSlugTokenPattern.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return text
	}
	var output strings.Builder
	last := 0
	for _, match := range matches {
		start, end := match[0], match[1]
		output.WriteString(text[last:start])
		token := text[start:end]
		reference := strings.Trim(token, "`")
		fingerprint := fmt.Sprintf("%x", sha256.Sum256([]byte(strings.ToLower(reference))))
		resolvedURL, ok := resolvedURLs[fingerprint]
		if !ok || insideSlackLink(text, start) {
			output.WriteString(token)
		} else if slackLink {
			output.WriteString("<" + resolvedURL + "|Agent Wiki source>")
		} else {
			output.WriteString(resolvedURL)
		}
		last = end
	}
	output.WriteString(text[last:])
	return output.String()
}

func insideSlackLink(text string, index int) bool {
	open := strings.LastIndex(text[:index], "<")
	close := strings.LastIndex(text[:index], ">")
	return open > close
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

// CompactPublishedArtifactSummary makes the durable document, rather than a
// second copy of it in Slack, the canonical long-form result. A model can
// still produce a useful title and synopsis, but any
// successful same-attempt artifact publication is represented exactly once by
// a typed artifact segment even when the model forgot that part of the output
// contract.
func CompactPublishedArtifactSummary(result types.SlackResult, producedURLs map[string]struct{}) types.SlackResult {
	if len(producedURLs) == 0 {
		return result
	}
	urls := make([]string, 0, len(producedURLs))
	for artifactURL := range producedURLs {
		urls = append(urls, artifactURL)
	}
	sort.Strings(urls)

	var header, synopsis *types.SlackSegment
	artifacts := make(map[string]types.SlackArtifact, len(urls))
	for _, segment := range result.Segments {
		switch segment.Kind {
		case types.SlackSegmentHeader:
			if header == nil {
				copy := segment
				header = &copy
			}
		case types.SlackSegmentMRKDWN:
			if synopsis == nil {
				copy := segment
				synopsis = &copy
			}
		case types.SlackSegmentArtifact:
			if segment.Artifact != nil {
				if _, ok := producedURLs[segment.Artifact.URL]; ok {
					artifacts[segment.Artifact.URL] = *segment.Artifact
				}
			}
		}
	}

	segments := make([]types.SlackSegment, 0, 2+len(urls))
	if header != nil {
		segments = append(segments, *header)
	}
	if synopsis != nil {
		copy := *synopsis
		copy.Text = compactPublishedArtifactSynopsis(stripPublishedArtifactLinks(copy.Text, urls))
		if copy.Text != "" {
			segments = append(segments, copy)
		}
	}
	for _, artifactURL := range urls {
		artifact, ok := artifacts[artifactURL]
		if !ok {
			artifact = types.SlackArtifact{Name: "Open the published document", MediaType: "text/html", URL: artifactURL}
		}
		segments = append(segments, types.SlackSegment{Kind: types.SlackSegmentArtifact, Artifact: &artifact})
	}
	result.Segments = segments
	return result
}

func compactPublishedArtifactSynopsis(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}

	// Publication acknowledgements should communicate the result, not mirror a
	// worker transcript. Keep only the first paragraph and at most two complete
	// sentences; the canonical document and control-plane footer carry the rest.
	if split := regexp.MustCompile(`\n\s*\n`).Split(text, 2); len(split) > 0 {
		text = split[0]
	}
	text = strings.Join(strings.Fields(text), " ")
	runes := []rune(text)
	sentences := 0
	cut := len(runes)
	for index, character := range runes {
		if character != '.' && character != '!' && character != '?' {
			continue
		}
		if index+1 < len(runes) && !unicode.IsSpace(runes[index+1]) {
			continue
		}
		sentences++
		if sentences == 2 {
			cut = index + 1
			break
		}
	}
	runes = runes[:cut]
	if len(runes) > maxArtifactSynopsis {
		cut = maxArtifactSynopsis
		for cut > 0 && !unicode.IsSpace(runes[cut-1]) {
			cut--
		}
		if cut == 0 {
			cut = maxArtifactSynopsis
		}
		runes = append([]rune(strings.TrimSpace(string(runes[:cut]))), '…')
	}
	return strings.TrimSpace(string(runes))
}

func stripPublishedArtifactLinks(text string, urls []string) string {
	for _, artifactURL := range urls {
		link := regexp.MustCompile(`<` + regexp.QuoteMeta(artifactURL) + `(?:\|[^>]*)?>`)
		text = link.ReplaceAllString(text, "")
		text = strings.ReplaceAll(text, artifactURL, "")
	}
	lines := strings.Split(text, "\n")
	compacted := lines[:0]
	previousBlank := false
	for _, line := range lines {
		blank := strings.TrimSpace(line) == ""
		if blank && previousBlank {
			continue
		}
		compacted = append(compacted, strings.TrimRight(line, " \t"))
		previousBlank = blank
	}
	return strings.TrimSpace(strings.Join(compacted, "\n"))
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
	if err := ValidateWikiReferenceLinks(result); err != nil {
		return nil, err
	}
	footer := renderAgentFooter(result.AgentFooter)
	blockLimit := maxBlocksPerMessage
	if footer != "" {
		// Reserve one block in every split payload so the final payload always
		// has room for the control-plane-owned provenance footer.
		blockLimit--
	}
	payloads := []Payload{{}}
	appendBlock := func(block map[string]any, fallback string) {
		last := &payloads[len(payloads)-1]
		if len(last.Blocks) >= blockLimit {
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
		if len(last.Blocks) > 0 && (len(last.Blocks)+len(blocks) > blockLimit || (containsTable(blocks) && containsTable(last.Blocks))) {
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
			if segment.Table != nil || segment.Card != nil || segment.Carousel != nil || segment.Image != nil || segment.Artifact != nil || segment.Approval != nil || segment.Notice != nil || strings.TrimSpace(segment.Text) == "" || !utf8.ValidString(segment.Text) || strings.ContainsRune(segment.Text, '\x00') || utf8.RuneCountInString(segment.Text) > maxHeaderCharacters {
				return nil, fmt.Errorf("%w: segment %d has invalid header payload", ErrInvalidResult, index)
			}
			appendBlock(map[string]any{
				"type": "header",
				"text": map[string]any{"type": "plain_text", "text": segment.Text, "emoji": true},
			}, segment.Text)
		case types.SlackSegmentMRKDWN:
			if segment.Table != nil || segment.Card != nil || segment.Carousel != nil || segment.Image != nil || segment.Artifact != nil || segment.Approval != nil || segment.Notice != nil {
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
					"type":   "section",
					"text":   map[string]any{"type": "mrkdwn", "text": chunk},
					"expand": true,
				}, stripFormatting(chunk))
			}
		case types.SlackSegmentContext:
			if segment.Table != nil || segment.Card != nil || segment.Carousel != nil || segment.Image != nil || segment.Artifact != nil || segment.Approval != nil || segment.Notice != nil || utf8.RuneCountInString(segment.Text) > maxContextCharacters {
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
			if segment.Text != "" || segment.Table != nil || segment.Card != nil || segment.Carousel != nil || segment.Image != nil || segment.Artifact != nil || segment.Approval != nil || segment.Notice != nil {
				return nil, fmt.Errorf("%w: segment %d has invalid divider payload", ErrInvalidResult, index)
			}
			appendBlock(map[string]any{"type": "divider"}, "")
		case types.SlackSegmentTable:
			if segment.Table == nil || segment.Text != "" || segment.Card != nil || segment.Carousel != nil || segment.Image != nil || segment.Artifact != nil || segment.Approval != nil || segment.Notice != nil {
				return nil, fmt.Errorf("%w: segment %d has invalid table payload", ErrInvalidResult, index)
			}
			tables, err := renderTables(*segment.Table)
			if err != nil {
				return nil, fmt.Errorf("segment %d: %w", index, err)
			}
			for tableIndex, table := range tables {
				last := &payloads[len(payloads)-1]
				if containsTable(last.Blocks) || len(last.Blocks) >= blockLimit {
					payloads = append(payloads, Payload{})
				}
				appendBlock(table, tableFallback(*segment.Table, tableIndex, len(tables)))
			}
		case types.SlackSegmentCard:
			if segment.Card == nil || segment.Text != "" || segment.Table != nil || segment.Carousel != nil || segment.Image != nil || segment.Artifact != nil || segment.Approval != nil || segment.Notice != nil {
				return nil, fmt.Errorf("%w: segment %d has invalid card payload", ErrInvalidResult, index)
			}
			card, fallback, err := renderCard(*segment.Card, fmt.Sprintf("tos_tag_card_%d", index))
			if err != nil {
				return nil, fmt.Errorf("segment %d: %w", index, err)
			}
			appendBlock(card, fallback)
		case types.SlackSegmentCarousel:
			if segment.Carousel == nil || segment.Text != "" || segment.Table != nil || segment.Card != nil || segment.Image != nil || segment.Artifact != nil || segment.Approval != nil || segment.Notice != nil {
				return nil, fmt.Errorf("%w: segment %d has invalid carousel payload", ErrInvalidResult, index)
			}
			if len(segment.Carousel.Cards) < 1 || len(segment.Carousel.Cards) > maxCarouselCards {
				return nil, fmt.Errorf("%w: carousel must contain 1-%d cards", ErrInvalidResult, maxCarouselCards)
			}
			elements := make([]any, 0, len(segment.Carousel.Cards))
			fallbacks := make([]string, 0, len(segment.Carousel.Cards))
			for cardIndex, item := range segment.Carousel.Cards {
				card, fallback, err := renderCard(item, "")
				if err != nil {
					return nil, fmt.Errorf("segment %d card %d: %w", index, cardIndex, err)
				}
				elements = append(elements, card)
				fallbacks = append(fallbacks, fallback)
			}
			appendBlock(map[string]any{"type": "carousel", "block_id": fmt.Sprintf("tos_tag_carousel_%d", index), "elements": elements}, strings.Join(fallbacks, "\n"))
		case types.SlackSegmentImage:
			if segment.Image == nil || segment.Text != "" || segment.Table != nil || segment.Card != nil || segment.Carousel != nil || segment.Artifact != nil || segment.Approval != nil || segment.Notice != nil {
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
			if segment.Artifact == nil || segment.Text != "" || segment.Table != nil || segment.Card != nil || segment.Carousel != nil || segment.Image != nil || segment.Approval != nil || segment.Notice != nil {
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
			if segment.Approval == nil || segment.Text != "" || segment.Table != nil || segment.Card != nil || segment.Carousel != nil || segment.Image != nil || segment.Artifact != nil || segment.Notice != nil {
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
			if segment.Notice == nil || segment.Text != "" || segment.Table != nil || segment.Card != nil || segment.Carousel != nil || segment.Image != nil || segment.Artifact != nil || segment.Approval != nil {
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
	if footer != "" {
		last := &payloads[len(payloads)-1]
		last.Blocks = append(last.Blocks, map[string]any{
			"type":     "context",
			"block_id": "tos_tag_agent_footer",
			"elements": []any{map[string]any{"type": "mrkdwn", "text": footer}},
		})
		last.Text += "\n" + footer
	}
	return payloads, nil
}

func renderAgentFooter(metadata *types.SlackAgentFooter) string {
	if metadata == nil || strings.TrimSpace(metadata.ModelID) == "" {
		return ""
	}
	parts := []string{humanizeModelID(metadata.ModelID)}
	if effort := strings.TrimSpace(metadata.ReasoningEffort); effort != "" {
		parts = append(parts, strings.ToLower(effort)+" effort")
	}
	totalTokens := metadata.TotalTokens
	if totalTokens <= 0 {
		totalTokens = metadata.InputTokens + metadata.OutputTokens
	}
	if totalTokens > 0 {
		parts = append(parts, compactCount(totalTokens)+" tokens")
	}
	if metadata.DurationMS > 0 {
		parts = append(parts, compactDuration(metadata.DurationMS))
	}
	return strings.Join(parts, "  •  ")
}

func humanizeModelID(modelID string) string {
	modelID = strings.TrimSpace(strings.TrimPrefix(modelID, "openai/"))
	if strings.HasPrefix(modelID, "gpt-") {
		modelID = "ChatGPT " + strings.TrimPrefix(modelID, "gpt-")
	}
	words := strings.FieldsFunc(modelID, func(character rune) bool { return character == '-' || character == '_' })
	for index, word := range words {
		if index == 0 && strings.EqualFold(word, "ChatGPT") {
			words[index] = "ChatGPT"
			continue
		}
		if _, err := strconv.ParseFloat(word, 64); err == nil {
			continue
		}
		words[index] = strings.ToUpper(word[:1]) + word[1:]
	}
	return strings.Join(words, " ")
}

func compactCount(value int64) string {
	if value < 1_000 {
		return strconv.FormatInt(value, 10)
	}
	if value < 10_000 {
		return strings.TrimSuffix(strconv.FormatFloat(float64(value)/1_000, 'f', 1, 64), ".0") + "k"
	}
	return strconv.FormatInt((value+500)/1_000, 10) + "k"
}

func compactDuration(milliseconds int64) string {
	if milliseconds < 1_000 {
		return strconv.FormatInt(milliseconds, 10) + "ms"
	}
	if milliseconds < 60_000 {
		return strings.TrimSuffix(strconv.FormatFloat(float64(milliseconds)/1_000, 'f', 1, 64), ".0") + "s"
	}
	duration := time.Duration(milliseconds) * time.Millisecond
	minutes := int64(duration / time.Minute)
	seconds := int64((duration % time.Minute) / time.Second)
	if seconds == 0 {
		return strconv.FormatInt(minutes, 10) + "m"
	}
	return fmt.Sprintf("%dm %ds", minutes, seconds)
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
	if containsDoubleAsteriskOutsideCode(text) {
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

func renderCard(card types.SlackCard, blockID string) (map[string]any, string, error) {
	if err := validateCard(card); err != nil {
		return nil, "", err
	}
	block := map[string]any{
		"type":  "card",
		"title": map[string]any{"type": "mrkdwn", "text": card.Title},
		"body":  map[string]any{"type": "mrkdwn", "text": card.Body},
	}
	if blockID != "" {
		block["block_id"] = blockID
	}
	if card.Subtitle != "" {
		block["subtitle"] = map[string]any{"type": "mrkdwn", "text": card.Subtitle}
	}
	if card.Subtext != "" {
		block["subtext"] = map[string]any{"type": "mrkdwn", "text": card.Subtext}
	}
	if card.Icon != nil {
		block["icon"] = renderCardImage(*card.Icon)
	}
	if card.HeroImage != nil {
		block["hero_image"] = renderCardImage(*card.HeroImage)
	}
	parts := make([]string, 0, 4)
	for _, text := range []string{card.Title, card.Subtitle, card.Body, card.Subtext} {
		if text = strings.TrimSpace(stripFormatting(text)); text != "" {
			parts = append(parts, text)
		}
	}
	fallback := strings.Join(parts, " — ")
	return block, fallback, nil
}

func renderCardImage(image types.SlackCardImage) map[string]any {
	return map[string]any{"type": "image", "image_url": image.URL, "alt_text": image.AltText}
}

func validateCard(card types.SlackCard) error {
	if strings.TrimSpace(card.Title) == "" || utf8.RuneCountInString(card.Title) > maxCardTitleCharacters {
		return fmt.Errorf("%w: card title is invalid", ErrInvalidResult)
	}
	if strings.TrimSpace(card.Body) == "" || utf8.RuneCountInString(card.Body) > maxCardTextCharacters {
		return fmt.Errorf("%w: card body is invalid", ErrInvalidResult)
	}
	if utf8.RuneCountInString(card.Subtitle) > maxCardTitleCharacters {
		return fmt.Errorf("%w: card subtitle is invalid", ErrInvalidResult)
	}
	if utf8.RuneCountInString(card.Subtext) > maxCardTextCharacters {
		return fmt.Errorf("%w: card subtext is invalid", ErrInvalidResult)
	}
	for _, text := range []string{card.Title, card.Subtitle, card.Body, card.Subtext} {
		if !utf8.ValidString(text) || strings.ContainsRune(text, '\x00') {
			return fmt.Errorf("%w: card body is invalid", ErrInvalidResult)
		}
		if text == "" {
			continue
		}
		if err := validateMRKDWN(text, types.SlackMentionAllowlist{}); err != nil {
			return err
		}
	}
	for _, image := range []*types.SlackCardImage{card.Icon, card.HeroImage} {
		if image == nil {
			continue
		}
		parsed, err := url.Parse(image.URL)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return fmt.Errorf("%w: card image URL must be HTTPS", ErrInvalidResult)
		}
		if strings.TrimSpace(image.AltText) == "" || !utf8.ValidString(image.AltText) || strings.ContainsRune(image.AltText, '\x00') || utf8.RuneCountInString(image.AltText) > maxImageCharacters {
			return fmt.Errorf("%w: card image alt text is invalid", ErrInvalidResult)
		}
	}
	return nil
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
	if table.Caption == "" && (table.PageSize != 0 || table.RowHeaderColumnIndex != 0) {
		return nil, fmt.Errorf("%w: data table caption is invalid", ErrInvalidResult)
	}
	if table.Caption != "" && len(table.Rows) == 0 {
		return nil, fmt.Errorf("%w: data table caption requires at least one data row", ErrInvalidResult)
	}
	if table.Caption != "" {
		return renderDataTable(table)
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

func renderDataTable(table types.SlackTable) ([]map[string]any, error) {
	if strings.TrimSpace(table.Caption) == "" || !utf8.ValidString(table.Caption) || strings.ContainsRune(table.Caption, '\x00') || utf8.RuneCountInString(table.Caption) > maxCardTextCharacters {
		return nil, fmt.Errorf("%w: data table caption is invalid", ErrInvalidResult)
	}
	if table.PageSize < 0 || table.PageSize > 100 {
		return nil, fmt.Errorf("%w: data table page size must be 1-100 when set", ErrInvalidResult)
	}
	if table.RowHeaderColumnIndex < 0 || table.RowHeaderColumnIndex >= len(table.Columns) {
		return nil, fmt.Errorf("%w: data table row header column is invalid", ErrInvalidResult)
	}
	if tableHeaderCharacters(table.Columns) > maxTableCharacters {
		return nil, fmt.Errorf("%w: table header and row exceed character limit", ErrInvalidResult)
	}
	rows := make([]any, 0, len(table.Rows)+1)
	header := make([]any, len(table.Columns))
	for index, column := range table.Columns {
		header[index] = map[string]any{"type": "raw_text", "text": column.Header}
	}
	rows = append(rows, header)
	characters := tableHeaderCharacters(table.Columns)
	for rowIndex, source := range table.Rows {
		rowChars := tableRowCharacters(source)
		if rowChars > maxTableCharacters || characters+rowChars > maxTableCharacters {
			return nil, fmt.Errorf("%w: row %d exceeds table character limit", ErrInvalidResult, rowIndex)
		}
		row, err := renderRow(source)
		if err != nil {
			return nil, fmt.Errorf("row %d: %w", rowIndex, err)
		}
		rows = append(rows, row)
		characters += rowChars
	}
	block := map[string]any{
		"type":                    "data_table",
		"block_id":                "tos_tag_data_table",
		"caption":                 table.Caption,
		"row_header_column_index": table.RowHeaderColumnIndex,
		"rows":                    rows,
	}
	if table.PageSize > 0 {
		block["page_size"] = table.PageSize
	}
	return []map[string]any{block}, nil
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
			if cell.Text != "" {
				if err := validateMRKDWN(cell.Text, types.SlackMentionAllowlist{}); err != nil {
					return nil, err
				}
			}
			if hasInlineSlackFormatting(cell.Text) {
				row[i] = renderRichTextCell(cell.Text)
			} else {
				row[i] = map[string]any{"type": "raw_text", "text": cell.Text}
			}
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
			row[i] = renderRichTextCell(cell.Text)
		default:
			return nil, fmt.Errorf("%w: invalid table cell type %q", ErrInvalidResult, cell.Type)
		}
	}
	return row, nil
}

func hasInlineSlackFormatting(text string) bool {
	if slackLinkPattern.MatchString(text) {
		return true
	}
	for _, marker := range []string{"`", "*", "_", "~"} {
		if first := strings.Index(text, marker); first >= 0 && strings.Contains(text[first+1:], marker) {
			return true
		}
	}
	return false
}

func renderRichTextCell(text string) map[string]any {
	return map[string]any{
		"type": "rich_text",
		"elements": []any{map[string]any{
			"type":     "rich_text_section",
			"elements": renderRichTextElements(text),
		}},
	}
}

func renderRichTextElements(text string) []any {
	elements := make([]any, 0, 4)
	styles := map[byte]string{'*': "bold", '_': "italic", '~': "strike", '`': "code"}
	active := make(map[string]bool, len(styles))
	var buffer strings.Builder
	styleValue := func() map[string]any {
		style := make(map[string]any, len(active))
		for name, enabled := range active {
			if enabled {
				style[name] = true
			}
		}
		return style
	}
	flush := func() {
		if buffer.Len() == 0 {
			return
		}
		element := map[string]any{"type": "text", "text": buffer.String()}
		if style := styleValue(); len(style) > 0 {
			element["style"] = style
		}
		elements = append(elements, element)
		buffer.Reset()
	}
	for index := 0; index < len(text); {
		if text[index] == '<' {
			match := slackLinkPattern.FindStringSubmatchIndex(text[index:])
			if len(match) == 6 && match[0] == 0 {
				flush()
				link := map[string]any{"type": "link", "url": text[index+match[2] : index+match[3]], "text": text[index+match[4] : index+match[5]]}
				if style := styleValue(); len(style) > 0 {
					link["style"] = style
				}
				elements = append(elements, link)
				index += match[1]
				continue
			}
		}
		if styleName, marker := styles[text[index]]; marker {
			if active[styleName] || strings.Contains(text[index+1:], string(text[index])) {
				flush()
				active[styleName] = !active[styleName]
				index++
				continue
			}
		}
		runeValue, size := utf8.DecodeRuneInString(text[index:])
		buffer.WriteRune(runeValue)
		index += size
	}
	flush()
	if len(elements) == 0 {
		return []any{map[string]any{"type": "text", "text": text}}
	}
	return elements
}

func containsTable(blocks []map[string]any) bool {
	for _, block := range blocks {
		if block["type"] == "table" || block["type"] == "data_table" {
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
	if table.Caption != "" {
		label = table.Caption
	}
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
