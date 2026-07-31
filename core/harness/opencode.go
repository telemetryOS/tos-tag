package harness

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/telemetryos/tos-tag/types"
)

type OpenCodeOptions struct {
	Enabled  bool
	BaseURL  string
	Username string
	Password string
	Timeout  time.Duration
}

type OpenCode struct {
	baseURL  *url.URL
	username string
	password string
	client   *http.Client
}

// NewOpenCode requires an explicit opt-in. Constructing it performs no I/O.
func NewOpenCode(options OpenCodeOptions) (*OpenCode, error) {
	if !options.Enabled {
		return nil, fmt.Errorf("OpenCode adapter requires explicit opt-in")
	}
	parsed, err := url.Parse(options.BaseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return nil, fmt.Errorf("invalid OpenCode base URL")
	}
	if options.Timeout <= 0 {
		options.Timeout = 30 * time.Second
	}
	return &OpenCode{baseURL: parsed, username: options.Username, password: options.Password, client: &http.Client{Timeout: options.Timeout}}, nil
}

func (o *OpenCode) Health(ctx context.Context) error {
	var result map[string]any
	return o.doJSON(ctx, http.MethodGet, "/global/health", nil, &result)
}

func (o *OpenCode) CreateSession(ctx context.Context, title string) (Session, error) {
	var result struct {
		ID    string `json:"id"`
		Title string `json:"title"`
	}
	err := o.doJSON(ctx, http.MethodPost, "/session", map[string]string{"title": title}, &result)
	if err != nil {
		return Session{}, err
	}
	if result.ID == "" {
		return Session{}, fmt.Errorf("OpenCode returned an empty session ID")
	}
	return Session{ID: result.ID, Title: result.Title, CreatedAt: time.Now().UTC()}, nil
}

func (o *OpenCode) Prompt(ctx context.Context, sessionID string, prompt Prompt) error {
	if sessionID == "" || prompt.RequestID == "" || prompt.Model == "" {
		return fmt.Errorf("session, request ID, and model are required")
	}
	providerID, modelID, ok := strings.Cut(prompt.Model, "/")
	if !ok || providerID == "" || modelID == "" {
		return fmt.Errorf("model must be provider/model")
	}
	body := map[string]any{
		"messageID": openCodeMessageID(prompt.RequestID),
		"model":     map[string]string{"providerID": providerID, "modelID": modelID},
		"parts":     []map[string]string{{"type": "text", "text": prompt.Text}},
	}
	if prompt.System != "" {
		body["system"] = prompt.System
	}
	if prompt.Variant != "" {
		body["variant"] = prompt.Variant
	}
	return o.doJSON(ctx, http.MethodPost, "/session/"+url.PathEscape(sessionID)+"/prompt_async", body, nil)
}

func openCodeMessageID(requestID string) string {
	sum := sha256.Sum256([]byte(requestID))
	return fmt.Sprintf("msg%x", sum[:12])
}

func (o *OpenCode) Permission(ctx context.Context, sessionID string, decision PermissionDecision) error {
	if sessionID == "" || decision.PermissionID == "" {
		return fmt.Errorf("session and permission IDs are required")
	}
	path := "/session/" + url.PathEscape(sessionID) + "/permissions/" + url.PathEscape(decision.PermissionID)
	response := "reject"
	if decision.Approved {
		response = "once"
	}
	return o.doJSON(ctx, http.MethodPost, path, map[string]string{"response": response}, nil)
}

func (o *OpenCode) Abort(ctx context.Context, sessionID string) error {
	return o.doJSON(ctx, http.MethodPost, "/session/"+url.PathEscape(sessionID)+"/abort", map[string]any{}, nil)
}

func (o *OpenCode) Events(ctx context.Context, sessionID string) (<-chan Event, <-chan error) {
	out := make(chan Event)
	errs := make(chan error, 1)
	go func() {
		defer close(out)
		defer close(errs)
		request, err := o.request(ctx, http.MethodGet, "/event?session_id="+url.QueryEscape(sessionID), nil)
		if err != nil {
			errs <- err
			return
		}
		request.Header.Set("Accept", "text/event-stream")
		response, err := o.client.Do(request)
		if err != nil {
			errs <- err
			return
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			errs <- responseError(response)
			return
		}
		scanner := bufio.NewScanner(response.Body)
		scanner.Buffer(make([]byte, 4096), 1<<20)
		partTypes := make(map[string]string)
		for scanner.Scan() {
			line := scanner.Text()
			if !strings.HasPrefix(line, "data:") {
				continue
			}
			event, err := decodeEvent(bytes.TrimSpace([]byte(strings.TrimPrefix(line, "data:"))), sessionID)
			if err != nil {
				errs <- fmt.Errorf("decode OpenCode event: %w", err)
				return
			}
			if event.Type == "message.part.updated" {
				if part, ok := event.Data["part"].(map[string]any); ok {
					id, _ := part["id"].(string)
					kind, _ := part["type"].(string)
					if id != "" && kind != "" {
						partTypes[id] = kind
					}
				}
			}
			if event.Type == "message.part.delta" {
				partID, _ := event.Data["partID"].(string)
				delta, _ := event.Data["delta"].(string)
				if partTypes[partID] == "text" && delta != "" {
					event.Type = "message.delta"
					event.Data = map[string]any{"text": delta, "part_id": partID, "upstream_type": "message.part.delta"}
				}
			}
			select {
			case out <- event:
			case <-ctx.Done():
				errs <- ctx.Err()
				return
			}
		}
		if err := scanner.Err(); err != nil && !errors.Is(err, context.Canceled) {
			errs <- err
		}
	}()
	return out, errs
}

func (o *OpenCode) doJSON(ctx context.Context, method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	request, err := o.request(ctx, method, path, body)
	if err != nil {
		return err
	}
	if input != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := o.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return responseError(response)
	}
	if output == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 1<<20))
	if err := decoder.Decode(output); err != nil {
		return fmt.Errorf("decode OpenCode response: %w", err)
	}
	return nil
}

func (o *OpenCode) request(ctx context.Context, method, path string, body io.Reader) (*http.Request, error) {
	relative, err := url.Parse(path)
	if err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, o.baseURL.ResolveReference(relative).String(), body)
	if err != nil {
		return nil, err
	}
	if o.username != "" || o.password != "" {
		request.SetBasicAuth(o.username, o.password)
	}
	return request, nil
}

func decodeEvent(data []byte, sessionID string) (Event, error) {
	var upstream struct {
		Type       string         `json:"type"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(data, &upstream); err != nil {
		return Event{}, err
	}
	if upstream.Type != "" && upstream.Properties != nil {
		id, _ := upstream.Properties["id"].(string)
		if id == "" {
			id = types.NewID("event")
		}
		return Event{ID: id, SessionID: sessionID, Type: upstream.Type, Data: upstream.Properties, CreatedAt: time.Now().UTC()}, nil
	}
	var direct Event
	if err := json.Unmarshal(data, &direct); err != nil {
		return Event{}, err
	}
	if direct.Type != "" && (direct.ID != "" || direct.SessionID != "" || direct.Data != nil) {
		if direct.SessionID == "" {
			direct.SessionID = sessionID
		}
		if direct.CreatedAt.IsZero() {
			direct.CreatedAt = time.Now().UTC()
		}
		return direct, nil
	}
	if upstream.Type == "" {
		return Event{}, fmt.Errorf("event type is missing")
	}
	id, _ := upstream.Properties["id"].(string)
	if id == "" {
		id = types.NewID("event")
	}
	return Event{ID: id, SessionID: sessionID, Type: upstream.Type, Data: upstream.Properties, CreatedAt: time.Now().UTC()}, nil
}

func responseError(response *http.Response) error {
	data, _ := io.ReadAll(io.LimitReader(response.Body, 16<<10))
	return fmt.Errorf("OpenCode HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(data)))
}
