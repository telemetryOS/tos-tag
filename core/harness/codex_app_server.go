package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
)

// Codex App Server speaks bidirectional JSON-RPC 2.0 without the jsonrpc
// envelope over newline-delimited stdio. This client owns only that transport;
// job policy and lifecycle remain in WorkerCodex.
type codexAppServer struct {
	stdin  io.WriteCloser
	stdout io.ReadCloser

	writeMu sync.Mutex
	mu      sync.Mutex
	nextID  atomic.Uint64
	pending map[string]chan codexRPCResponse
	done    chan struct{}
	closed  bool

	onNotification func(string, json.RawMessage)
	onRequest      func(context.Context, string, json.RawMessage) (any, error)
	onFailure      func(error)
}

type codexRPCMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *codexRPCError  `json:"error,omitempty"`
}

type codexRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type codexRPCResponse struct {
	result json.RawMessage
	err    error
}

type CodexProtocolError struct{ Code string }

func (e *CodexProtocolError) Error() string          { return "Codex App Server request failed: " + e.Code }
func (e *CodexProtocolError) DiagnosticCode() string { return e.Code }

func newCodexAppServer(stdin io.WriteCloser, stdout io.ReadCloser, onNotification func(string, json.RawMessage), onRequest func(context.Context, string, json.RawMessage) (any, error), onFailure func(error)) (*codexAppServer, error) {
	if stdin == nil || stdout == nil {
		return nil, errors.New("Codex App Server stdio is required")
	}
	client := &codexAppServer{stdin: stdin, stdout: stdout, pending: make(map[string]chan codexRPCResponse), done: make(chan struct{}), onNotification: onNotification, onRequest: onRequest, onFailure: onFailure}
	go client.readLoop()
	return client, nil
}

func (c *codexAppServer) initialize(ctx context.Context) error {
	var result map[string]any
	if err := c.call(ctx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "tos_tag", "title": "TelemetryOS Tag", "version": "0.1.0"},
		"capabilities": map[string]any{"experimentalApi": true},
	}, &result); err != nil {
		return err
	}
	return c.notify("initialized", map[string]any{})
}

func (c *codexAppServer) call(ctx context.Context, method string, params any, output any) error {
	id := c.nextID.Add(1)
	key := strconv.FormatUint(id, 10)
	response := make(chan codexRPCResponse, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return &CodexProtocolError{Code: "transport_closed"}
	}
	c.pending[key] = response
	c.mu.Unlock()
	if err := c.write(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		c.removePending(key)
		return err
	}
	select {
	case value := <-response:
		if value.err != nil {
			return value.err
		}
		if output == nil || len(value.result) == 0 || string(value.result) == "null" {
			return nil
		}
		if err := json.Unmarshal(value.result, output); err != nil {
			return &CodexProtocolError{Code: "invalid_response"}
		}
		return nil
	case <-ctx.Done():
		c.removePending(key)
		return ctx.Err()
	case <-c.done:
		c.removePending(key)
		return &CodexProtocolError{Code: "transport_closed"}
	}
}

func (c *codexAppServer) notify(method string, params any) error {
	return c.write(map[string]any{"method": method, "params": params})
}

func (c *codexAppServer) write(message any) error {
	encoded, err := json.Marshal(message)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.mu.Lock()
	closed := c.closed
	c.mu.Unlock()
	if closed {
		return &CodexProtocolError{Code: "transport_closed"}
	}
	if _, err := c.stdin.Write(encoded); err != nil {
		c.fail(&CodexProtocolError{Code: "transport_write"})
		return &CodexProtocolError{Code: "transport_write"}
	}
	return nil
}

func (c *codexAppServer) readLoop() {
	scanner := bufio.NewScanner(c.stdout)
	scanner.Buffer(make([]byte, 64<<10), 16<<20)
	for scanner.Scan() {
		var message codexRPCMessage
		if err := json.Unmarshal(scanner.Bytes(), &message); err != nil {
			c.fail(&CodexProtocolError{Code: "invalid_message"})
			return
		}
		if message.Method != "" && len(message.ID) > 0 {
			go c.handleServerRequest(message)
			continue
		}
		if message.Method != "" {
			if c.onNotification != nil {
				c.onNotification(message.Method, message.Params)
			}
			continue
		}
		if len(message.ID) > 0 {
			c.handleResponse(message)
		}
	}
	if err := scanner.Err(); err != nil {
		c.fail(&CodexProtocolError{Code: "transport_read"})
		return
	}
	c.fail(&CodexProtocolError{Code: "transport_closed"})
}

func (c *codexAppServer) handleResponse(message codexRPCMessage) {
	key := rpcIDKey(message.ID)
	c.mu.Lock()
	response, ok := c.pending[key]
	if ok {
		delete(c.pending, key)
	}
	c.mu.Unlock()
	if !ok {
		return
	}
	if message.Error != nil {
		response <- codexRPCResponse{err: &CodexProtocolError{Code: "rpc_" + strconv.Itoa(message.Error.Code)}}
		return
	}
	response <- codexRPCResponse{result: message.Result}
}

func (c *codexAppServer) handleServerRequest(message codexRPCMessage) {
	var result any
	var err error
	if c.onRequest == nil {
		err = &CodexProtocolError{Code: "unsupported_server_request"}
	} else {
		result, err = c.onRequest(context.Background(), message.Method, message.Params)
	}
	if err != nil {
		_ = c.write(map[string]any{"id": json.RawMessage(message.ID), "error": map[string]any{"code": -32603, "message": "request failed"}})
		return
	}
	_ = c.write(map[string]any{"id": json.RawMessage(message.ID), "result": result})
}

func (c *codexAppServer) removePending(key string) {
	c.mu.Lock()
	delete(c.pending, key)
	c.mu.Unlock()
}

func (c *codexAppServer) fail(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	pending := c.pending
	c.pending = make(map[string]chan codexRPCResponse)
	close(c.done)
	c.mu.Unlock()
	for _, response := range pending {
		response <- codexRPCResponse{err: err}
	}
	if c.onFailure != nil {
		c.onFailure(err)
	}
}

func (c *codexAppServer) close() {
	_ = c.stdin.Close()
	_ = c.stdout.Close()
	c.fail(&CodexProtocolError{Code: "transport_closed"})
}

func rpcIDKey(raw json.RawMessage) string {
	return strings.Trim(string(raw), `"`)
}

func sanitizeCodexCode(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	var builder strings.Builder
	for _, character := range value {
		if (character >= 'a' && character <= 'z') || (character >= 'A' && character <= 'Z') || (character >= '0' && character <= '9') || character == '_' || character == '-' || character == '.' {
			builder.WriteRune(character)
		} else {
			builder.WriteByte('_')
		}
		if builder.Len() >= 64 {
			break
		}
	}
	if builder.Len() == 0 {
		return "unknown"
	}
	return builder.String()
}

func codexErrorCode(value any) string {
	if text, ok := value.(string); ok {
		return sanitizeCodexCode(text)
	}
	if object, ok := value.(map[string]any); ok {
		for key := range object {
			return sanitizeCodexCode(key)
		}
	}
	return "unknown"
}

func codexErrorMessageCode(message string) string {
	var envelope struct {
		Error struct {
			Type  string `json:"type"`
			Code  string `json:"code"`
			Param string `json:"param"`
		} `json:"error"`
	}
	if json.Unmarshal([]byte(message), &envelope) == nil {
		parts := make([]string, 0, 3)
		for _, value := range []string{envelope.Error.Type, envelope.Error.Code, envelope.Error.Param} {
			if value != "" {
				parts = append(parts, sanitizeCodexCode(value))
			}
		}
		if len(parts) > 0 {
			return strings.Join(parts, "_")
		}
	}
	return sanitizeCodexCode(message)
}
