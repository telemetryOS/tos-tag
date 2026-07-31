package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestFakeHarnessLifecycle(t *testing.T) {
	fake := NewFake()
	session, err := fake.CreateSession(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	prompt := Prompt{Text: "hello", Model: "fake/model", RequestID: "request-1", SlackFormat: "slack-mrkdwn/v1"}
	if err := fake.Prompt(context.Background(), session.ID, prompt); err != nil {
		t.Fatal(err)
	}
	events, errs := fake.Events(context.Background(), session.ID)
	var count int
	for range events {
		count++
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if count != 2 || len(fake.Prompts(session.ID)) != 1 {
		t.Fatalf("events=%d prompts=%d", count, len(fake.Prompts(session.ID)))
	}
	if err := fake.Abort(context.Background(), session.ID); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCodeAdapterContractAgainstFakeServer(t *testing.T) {
	var authorization string
	var promptBody map[string]any
	var permissionBody map[string]any
	mux := http.NewServeMux()
	mux.HandleFunc("GET /global/health", func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"healthy":true}`))
	})
	mux.HandleFunc("POST /session", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"session-1","title":"test"}`))
	})
	mux.HandleFunc("POST /session/session-1/prompt_async", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&promptBody); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /session/session-1/permissions/perm-1", func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&permissionBody); err != nil {
			t.Fatal(err)
		}
		w.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("POST /session/session-1/abort", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /event", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintln(w, `data: {"id":"event-1","session_id":"session-1","type":"session.idle"}`)
	})
	adapter, err := NewOpenCode(OpenCodeOptions{Enabled: true, BaseURL: "http://opencode.test", Username: "opencode", Password: "secret-token", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	adapter.client.Transport = handlerTransport{handler: mux}
	ctx := context.Background()
	if err := adapter.Health(ctx); err != nil {
		t.Fatal(err)
	}
	if authorization != "Basic b3BlbmNvZGU6c2VjcmV0LXRva2Vu" {
		t.Fatal("adapter did not authenticate the upstream request")
	}
	session, err := adapter.CreateSession(ctx, "test")
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Prompt(ctx, session.ID, Prompt{Text: "hello", Model: "provider/model", RequestID: "request-1"}); err != nil {
		t.Fatal(err)
	}
	if promptBody["messageID"] != openCodeMessageID("request-1") || promptBody["model"].(map[string]any)["providerID"] != "provider" {
		t.Fatalf("unexpected prompt body: %#v", promptBody)
	}
	if err := adapter.Permission(ctx, session.ID, PermissionDecision{PermissionID: "perm-1", Approved: false}); err != nil {
		t.Fatal(err)
	}
	if permissionBody["response"] != "reject" {
		t.Fatalf("unexpected permission: %#v", permissionBody)
	}
	events, errs := adapter.Events(ctx, session.ID)
	if event := <-events; event.Type != "session.idle" {
		t.Fatalf("event = %#v", event)
	}
	if err := <-errs; err != nil {
		t.Fatal(err)
	}
	if err := adapter.Abort(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
}

func TestOpenCodeRequiresOptInAndRejectsMalformedResponses(t *testing.T) {
	if _, err := NewOpenCode(OpenCodeOptions{BaseURL: "http://127.0.0.1"}); err == nil {
		t.Fatal("disabled OpenCode adapter was accepted")
	}
	adapter, err := NewOpenCode(OpenCodeOptions{Enabled: true, BaseURL: "http://opencode.test", Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	adapter.client.Transport = handlerTransport{handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`not-json`)) })}
	if err := adapter.Health(context.Background()); err == nil {
		t.Fatal("malformed response was accepted")
	}
}

type handlerTransport struct{ handler http.Handler }

func (t handlerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	recorder := httptest.NewRecorder()
	t.handler.ServeHTTP(recorder, request)
	response := recorder.Result()
	response.Request = request
	return response, nil
}
