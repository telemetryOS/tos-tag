package harness

import (
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestBuildWikiBridgeRequestConstructsReviewedArguments(t *testing.T) {
	tests := map[string]struct {
		request     wikiToolRequest
		operationID string
		arguments   []string
	}{
		"search": {
			request: wikiToolRequest{Operation: "search", Query: "Node Mini", Namespace: "primer", Limit: 20}, operationID: "read",
			arguments: []string{"search", "Node Mini", "--ns", "primer", "--limit", "20"},
		},
		"get revision": {
			request: wikiToolRequest{Operation: "get", PageReference: "primer/node-mini", Revision: 3}, operationID: "read",
			arguments: []string{"get", "primer/node-mini", "--json", "--rev", "3"},
		},
		"put markdown": {
			request: wikiToolRequest{Operation: "put", PageReference: "artifacts/guide", Title: "Guide", Body: "# Guide", Tags: []string{"guide", "ops"}, Note: "Refresh guide"}, operationID: "write",
			arguments: []string{"put", "artifacts/guide", "--title", "Guide", "--body", "# Guide", "--tags", "guide,ops", "--note", "Refresh guide", "--md", "--json"},
		},
		"append html": {
			request: wikiToolRequest{Operation: "append", PageReference: "artifacts/log", Body: "<p>entry</p>", Format: "html"}, operationID: "write",
			arguments: []string{"append", "artifacts/log", "--body", "<p>entry</p>", "--json"},
		},
		"revert": {
			request: wikiToolRequest{Operation: "revert", PageReference: "artifacts/guide", Revision: 2, Note: "Restore known good revision"}, operationID: "write",
			arguments: []string{"revert", "artifacts/guide", "--rev", "2", "--note", "Restore known good revision"},
		},
		"delete": {
			request: wikiToolRequest{Operation: "rm", PageReference: "artifacts/old", ApprovalID: "approval-1"}, operationID: "delete",
			arguments: []string{"rm", "artifacts/old"},
		},
	}
	for name, testCase := range tests {
		t.Run(name, func(t *testing.T) {
			invocation, encoded, err := buildWikiBridgeRequest(testCase.request)
			if err != nil {
				t.Fatal(err)
			}
			if invocation.ToolID != wikiToolID || invocation.OperationID != testCase.operationID || invocation.ResourceAction != testCase.request.Operation {
				t.Fatalf("invocation = %#v", invocation)
			}
			var bridge wikiBridgeRequest
			if err := json.Unmarshal(encoded, &bridge); err != nil {
				t.Fatal(err)
			}
			if bridge.ToolID != wikiToolID || bridge.OperationID != testCase.operationID || !reflect.DeepEqual(bridge.Arguments, testCase.arguments) || bridge.Wiki.Operation != testCase.request.Operation {
				t.Fatalf("bridge request = %#v", bridge)
			}
			if bridge.ApprovalID != testCase.request.ApprovalID {
				t.Fatalf("approval id = %q", bridge.ApprovalID)
			}
		})
	}
}

func TestBuildWikiBridgeRequestRejectsMalformedSemanticRequestsWithSafeCodes(t *testing.T) {
	tests := map[string]wikiToolRequest{
		"unsupported":      {Operation: "admin"},
		"missing page":     {Operation: "get"},
		"missing title":    {Operation: "put", PageReference: "artifacts/guide", Body: "body"},
		"missing body":     {Operation: "put", PageReference: "artifacts/guide", Title: "Guide"},
		"missing revision": {Operation: "revert", PageReference: "artifacts/guide"},
		"irrelevant field": {Operation: "url", PageReference: "artifacts/guide", Query: "leak"},
		"dash positional":  {Operation: "get", PageReference: "--help"},
		"approval on read": {Operation: "get", PageReference: "artifacts/guide", ApprovalID: "approval-1"},
		"invalid tag":      {Operation: "put", PageReference: "artifacts/guide", Title: "Guide", Body: "body", Tags: []string{"one,two"}},
		"oversize body":    {Operation: "append", PageReference: "artifacts/guide", Body: string(make([]byte, (768<<10)+1))},
	}
	for name, request := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := buildWikiBridgeRequest(request)
			var validationErr *wikiValidationError
			if !errors.As(err, &validationErr) || !safeValidationCode(validationErr.Code) {
				t.Fatalf("validation error = %#v", err)
			}
		})
	}
}

func TestDecodeWikiToolRequestRejectsUnknownFields(t *testing.T) {
	_, err := decodeWikiToolRequest(json.RawMessage(`{"skill_names":["wiki"],"operation":"get","page_reference":"primer/node-mini","arguments":["get"]}`))
	var validationErr *wikiValidationError
	if !errors.As(err, &validationErr) || validationErr.Code != "wiki.request.invalid_json" {
		t.Fatalf("decode error = %v", err)
	}
}

func TestValidationCodeFromBridgeOutputAcceptsOnlyClosedShape(t *testing.T) {
	if got := validationCodeFromBridgeOutput(`{"validation_code":"wiki.cli.missing_body"}`); got != "wiki.cli.missing_body" {
		t.Fatalf("validation code = %q", got)
	}
	for _, value := range []string{`{"validation_code":"private body text"}`, `{"validation_code":"wiki.cli.bad-value"}`, `not-json`} {
		if got := validationCodeFromBridgeOutput(value); got != "" {
			t.Fatalf("unsafe validation code = %q", got)
		}
	}
}
