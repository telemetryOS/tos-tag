package harness

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

const (
	wikiToolID      = "telemetryos.wiki"
	wikiDynamicTool = "tos_tag_wiki"
)

type wikiToolRequest struct {
	SkillNames    []string `json:"skill_names"`
	Operation     string   `json:"operation"`
	PageReference string   `json:"page_reference,omitempty"`
	Namespace     string   `json:"namespace,omitempty"`
	Query         string   `json:"query,omitempty"`
	Title         string   `json:"title,omitempty"`
	Body          string   `json:"body,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	Note          string   `json:"note,omitempty"`
	Prefix        string   `json:"prefix,omitempty"`
	Tag           string   `json:"tag,omitempty"`
	Format        string   `json:"format,omitempty"`
	Revision      int      `json:"revision,omitempty"`
	Limit         int      `json:"limit,omitempty"`
	Depth         int      `json:"depth,omitempty"`
	AllowEmpty    bool     `json:"allow_empty,omitempty"`
	ApprovalID    string   `json:"approval_id,omitempty"`
}

// wikiActionEnvelope is retained with an approval so a resumed worker receives
// the same typed Wiki operation it originally proposed. The reviewed bridge
// still verifies the exact Go-generated argv as part of the immutable action.
type wikiActionEnvelope struct {
	Operation     string   `json:"operation"`
	PageReference string   `json:"page_reference,omitempty"`
	Namespace     string   `json:"namespace,omitempty"`
	Query         string   `json:"query,omitempty"`
	Title         string   `json:"title,omitempty"`
	Body          string   `json:"body,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	Note          string   `json:"note,omitempty"`
	Prefix        string   `json:"prefix,omitempty"`
	Tag           string   `json:"tag,omitempty"`
	Format        string   `json:"format,omitempty"`
	Revision      int      `json:"revision,omitempty"`
	Limit         int      `json:"limit,omitempty"`
	Depth         int      `json:"depth,omitempty"`
	AllowEmpty    bool     `json:"allow_empty,omitempty"`
}

type wikiBridgeRequest struct {
	ToolID      string             `json:"tool_id"`
	OperationID string             `json:"operation_id"`
	Arguments   []string           `json:"arguments"`
	ApprovalID  string             `json:"approval_id,omitempty"`
	Wiki        wikiActionEnvelope `json:"wiki"`
}

// wikiValidationError intentionally contains only a closed, content-free code.
// It is safe to expose in progress telemetry and persist in audit metadata.
type wikiValidationError struct {
	Code      string
	Operation string
}

func (e *wikiValidationError) Error() string {
	return "typed Agent Wiki request rejected: " + e.Code
}

func decodeWikiToolRequest(arguments json.RawMessage) (wikiToolRequest, error) {
	decoder := json.NewDecoder(bytes.NewReader(arguments))
	decoder.DisallowUnknownFields()
	var request wikiToolRequest
	if err := decoder.Decode(&request); err != nil {
		return wikiToolRequest{}, &wikiValidationError{Code: "wiki.request.invalid_json"}
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return wikiToolRequest{}, &wikiValidationError{Code: "wiki.request.invalid_json"}
	}
	request.Operation = strings.ToLower(strings.TrimSpace(request.Operation))
	return request, nil
}

func buildWikiBridgeRequest(request wikiToolRequest) (declaredToolInvocation, json.RawMessage, error) {
	operationID, ok := wikiReviewedOperation(request.Operation)
	if !ok {
		return declaredToolInvocation{}, nil, &wikiValidationError{Code: "wiki.operation.unsupported", Operation: request.Operation}
	}
	invalid := func(code string) (declaredToolInvocation, json.RawMessage, error) {
		return declaredToolInvocation{}, nil, &wikiValidationError{Code: code, Operation: request.Operation}
	}
	if request.ApprovalID != "" && request.Operation != "rm" {
		return invalid("wiki.approval.not_allowed")
	}
	if err := validateWikiValues(request); err != nil {
		return invalid(err.Code)
	}

	args := []string{request.Operation}
	switch request.Operation {
	case "map":
		if wikiHasAnyPageField(request) {
			return invalid("wiki.field.not_allowed")
		}
	case "ls":
		if request.Namespace == "" {
			return invalid("wiki.namespace.required")
		}
		if wikiHasAny(request.PageReference, request.Query, request.Title, request.Body, request.Note, request.Format) || len(request.Tags) > 0 || request.Revision != 0 || request.Depth != 0 || request.AllowEmpty {
			return invalid("wiki.field.not_allowed")
		}
		args = append(args, request.Namespace)
		args = appendStringFlag(args, "--prefix", request.Prefix)
		args = appendStringFlag(args, "--tag", request.Tag)
		args = appendIntFlag(args, "--limit", request.Limit)
	case "tree":
		if request.Namespace == "" {
			return invalid("wiki.namespace.required")
		}
		if wikiHasAny(request.PageReference, request.Query, request.Title, request.Body, request.Note, request.Prefix, request.Tag, request.Format) || len(request.Tags) > 0 || request.Revision != 0 || request.Limit != 0 || request.AllowEmpty {
			return invalid("wiki.field.not_allowed")
		}
		args = append(args, request.Namespace)
		args = appendIntFlag(args, "--depth", request.Depth)
	case "search":
		if request.Query == "" {
			return invalid("wiki.query.required")
		}
		if wikiHasAny(request.PageReference, request.Title, request.Body, request.Note, request.Prefix, request.Tag, request.Format) || len(request.Tags) > 0 || request.Revision != 0 || request.Depth != 0 || request.AllowEmpty {
			return invalid("wiki.field.not_allowed")
		}
		args = append(args, request.Query)
		args = appendStringFlag(args, "--ns", request.Namespace)
		args = appendIntFlag(args, "--limit", request.Limit)
	case "get":
		if request.PageReference == "" {
			return invalid("wiki.page_reference.required")
		}
		if wikiHasAny(request.Namespace, request.Query, request.Title, request.Body, request.Note, request.Prefix, request.Tag, request.Format) || len(request.Tags) > 0 || request.Limit != 0 || request.Depth != 0 || request.AllowEmpty {
			return invalid("wiki.field.not_allowed")
		}
		args = append(args, request.PageReference, "--json")
		args = appendIntFlag(args, "--rev", request.Revision)
	case "url", "revs", "restore", "rm":
		if request.PageReference == "" {
			return invalid("wiki.page_reference.required")
		}
		if wikiHasAny(request.Namespace, request.Query, request.Title, request.Body, request.Note, request.Prefix, request.Tag, request.Format) || len(request.Tags) > 0 || request.Revision != 0 || request.Limit != 0 || request.Depth != 0 || request.AllowEmpty {
			return invalid("wiki.field.not_allowed")
		}
		args = append(args, request.PageReference)
	case "put":
		if request.PageReference == "" {
			return invalid("wiki.page_reference.required")
		}
		if strings.TrimSpace(request.Title) == "" {
			return invalid("wiki.title.required")
		}
		if strings.TrimSpace(request.Body) == "" && !request.AllowEmpty {
			return invalid("wiki.body.required")
		}
		if wikiHasAny(request.Namespace, request.Query, request.Prefix, request.Tag) || request.Revision != 0 || request.Limit != 0 || request.Depth != 0 {
			return invalid("wiki.field.not_allowed")
		}
		args = append(args, request.PageReference, "--title", request.Title, "--body", request.Body)
		if len(request.Tags) > 0 {
			args = append(args, "--tags", strings.Join(request.Tags, ","))
		}
		args = appendStringFlag(args, "--note", request.Note)
		args = appendWikiFormat(args, request.Format)
		if request.AllowEmpty {
			args = append(args, "--allow-empty")
		}
		args = append(args, "--json")
	case "append":
		if request.PageReference == "" {
			return invalid("wiki.page_reference.required")
		}
		if strings.TrimSpace(request.Body) == "" {
			return invalid("wiki.body.required")
		}
		if wikiHasAny(request.Namespace, request.Query, request.Title, request.Prefix, request.Tag) || len(request.Tags) > 0 || request.Revision != 0 || request.Limit != 0 || request.Depth != 0 || request.AllowEmpty {
			return invalid("wiki.field.not_allowed")
		}
		args = append(args, request.PageReference, "--body", request.Body)
		args = appendStringFlag(args, "--note", request.Note)
		args = appendWikiFormat(args, request.Format)
		args = append(args, "--json")
	case "revert":
		if request.PageReference == "" {
			return invalid("wiki.page_reference.required")
		}
		if request.Revision < 1 {
			return invalid("wiki.revision.required")
		}
		if wikiHasAny(request.Namespace, request.Query, request.Title, request.Body, request.Prefix, request.Tag, request.Format) || len(request.Tags) > 0 || request.Limit != 0 || request.Depth != 0 || request.AllowEmpty {
			return invalid("wiki.field.not_allowed")
		}
		args = append(args, request.PageReference, "--rev", fmt.Sprint(request.Revision))
		args = appendStringFlag(args, "--note", request.Note)
	}

	bridge := wikiBridgeRequest{
		ToolID: wikiToolID, OperationID: operationID, Arguments: args, ApprovalID: request.ApprovalID,
		Wiki: wikiActionEnvelope{
			Operation: request.Operation, PageReference: request.PageReference, Namespace: request.Namespace,
			Query: request.Query, Title: request.Title, Body: request.Body, Tags: append([]string(nil), request.Tags...),
			Note: request.Note, Prefix: request.Prefix, Tag: request.Tag, Format: request.Format,
			Revision: request.Revision, Limit: request.Limit, Depth: request.Depth, AllowEmpty: request.AllowEmpty,
		},
	}
	encoded, err := json.Marshal(bridge)
	if err != nil {
		return invalid("wiki.request.encoding_failed")
	}
	return declaredToolInvocation{ToolID: wikiToolID, OperationID: operationID, ResourceAction: request.Operation}, encoded, nil
}

func wikiReviewedOperation(operation string) (string, bool) {
	switch operation {
	case "map", "ls", "tree", "get", "search", "revs", "url":
		return "read", true
	case "put", "append", "restore", "revert":
		return "write", true
	case "rm":
		return "delete", true
	default:
		return "", false
	}
}

func validateWikiValues(request wikiToolRequest) *wikiValidationError {
	for _, value := range []struct {
		name  string
		value string
		max   int
	}{
		{"page_reference", request.PageReference, 2048}, {"namespace", request.Namespace, 128},
		{"query", request.Query, 2048}, {"title", request.Title, 512}, {"note", request.Note, 4096},
		{"prefix", request.Prefix, 256}, {"tag", request.Tag, 128}, {"approval", request.ApprovalID, 256},
	} {
		if len(value.value) > value.max || strings.ContainsRune(value.value, 0) || hasDisallowedControl(value.value) {
			return &wikiValidationError{Code: "wiki." + value.name + ".invalid", Operation: request.Operation}
		}
	}
	if len(request.Body) > 768<<10 || strings.ContainsRune(request.Body, 0) {
		return &wikiValidationError{Code: "wiki.body.invalid", Operation: request.Operation}
	}
	for _, value := range []string{request.PageReference, request.Namespace, request.Query} {
		if strings.HasPrefix(strings.TrimSpace(value), "-") {
			return &wikiValidationError{Code: "wiki.positional.invalid", Operation: request.Operation}
		}
	}
	if request.Format != "" && request.Format != "markdown" && request.Format != "html" {
		return &wikiValidationError{Code: "wiki.format.invalid", Operation: request.Operation}
	}
	if request.Revision < 0 || request.Limit < 0 || request.Limit > 200 || request.Depth < 0 || request.Depth > 20 {
		return &wikiValidationError{Code: "wiki.numeric_range.invalid", Operation: request.Operation}
	}
	if len(request.Tags) > 64 {
		return &wikiValidationError{Code: "wiki.tags.invalid", Operation: request.Operation}
	}
	for _, tag := range request.Tags {
		if strings.TrimSpace(tag) == "" || len(tag) > 128 || strings.Contains(tag, ",") || strings.ContainsRune(tag, 0) || hasDisallowedControl(tag) {
			return &wikiValidationError{Code: "wiki.tags.invalid", Operation: request.Operation}
		}
	}
	return nil
}

func hasDisallowedControl(value string) bool {
	for _, char := range value {
		if char < 0x20 && char != '\t' {
			return true
		}
	}
	return false
}

func wikiHasAny(values ...string) bool {
	for _, value := range values {
		if value != "" {
			return true
		}
	}
	return false
}

func wikiHasAnyPageField(request wikiToolRequest) bool {
	return wikiHasAny(request.PageReference, request.Namespace, request.Query, request.Title, request.Body, request.Note, request.Prefix, request.Tag, request.Format, request.ApprovalID) ||
		len(request.Tags) > 0 || request.Revision != 0 || request.Limit != 0 || request.Depth != 0 || request.AllowEmpty
}

func appendStringFlag(args []string, flag, value string) []string {
	if value == "" {
		return args
	}
	return append(args, flag, value)
}

func appendIntFlag(args []string, flag string, value int) []string {
	if value == 0 {
		return args
	}
	return append(args, flag, fmt.Sprint(value))
}

func normalizedWikiFormat(value string) string {
	if value == "" {
		return "markdown"
	}
	return value
}

func appendWikiFormat(args []string, value string) []string {
	if normalizedWikiFormat(value) == "markdown" {
		return append(args, "--md")
	}
	return args
}
