// Package tools validates and executes reviewed server-side tool bundles.
// It never evaluates a model-provided shell command string.
package tools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/telemetryos/tos-tag/core/usage"
)

var envName = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

type Manifest struct {
	ID         string      `json:"id"`
	Version    string      `json:"version"`
	Script     string      `json:"script"`
	Operations []Operation `json:"operations"`
}

type Operation struct {
	ID             string   `json:"id"`
	Env            []string `json:"env,omitempty"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	MaxOutputBytes int      `json:"max_output_bytes"`
	Risk           string   `json:"risk"`
	Approval       string   `json:"approval,omitempty"`
}

const (
	ApprovalRiskBased = ""
	ApprovalAlways    = "always"
	ApprovalNever     = "never"
)

// RequiresApproval keeps the legacy fail-closed behavior unless a reviewed
// operation manifest explicitly opts into a different policy. The model
// cannot select or override this value at execution time.
func (o Operation) RequiresApproval() bool {
	switch o.Approval {
	case ApprovalAlways:
		return true
	case ApprovalNever:
		return false
	default:
		return o.Risk != "read"
	}
}

type Bundle struct {
	Root        string
	Manifest    Manifest
	ContentHash string
	ScriptHash  string
}

type Capability struct {
	ToolID        string
	ToolVersion   string
	OperationID   string
	AttemptToken  string
	SteeringEpoch int64
	ExpiresAt     time.Time
}

type Request struct {
	OrganizationID string
	JobID          string
	OperationID    string
	Args           []string
	Capability     Capability
	SecretValues   map[string]string
}

type Result struct {
	Output   string        `json:"output"`
	ExitCode int           `json:"exit_code"`
	Duration time.Duration `json:"duration_ns"`
}

type Executor struct {
	Enabled bool
	Usage   usage.Recorder
	Path    string
}

func LoadBundle(root, manifestPath string) (Bundle, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return Bundle{}, err
	}
	if _, err := safeChild(absolute, manifestPath); err != nil {
		return Bundle{}, err
	}
	data, err := readRootFile(absolute, manifestPath, 1<<20)
	if err != nil {
		return Bundle{}, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	var manifest Manifest
	if err := decoder.Decode(&manifest); err != nil {
		return Bundle{}, err
	}
	if manifest.ID == "" || manifest.Version == "" || len(manifest.Operations) == 0 {
		return Bundle{}, errors.New("tool ID, version, and operations are required")
	}
	script, err := safeChild(absolute, manifest.Script)
	if err != nil {
		return Bundle{}, err
	}
	info, err := os.Lstat(script)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o111 == 0 {
		return Bundle{}, errors.New("tool script must be a real executable file")
	}
	seen := make(map[string]bool)
	for _, operation := range manifest.Operations {
		if operation.ID == "" || seen[operation.ID] || !validRisk(operation.Risk) || !validApprovalPolicy(operation.Approval) || operation.TimeoutSeconds <= 0 || operation.TimeoutSeconds > 300 || operation.MaxOutputBytes <= 0 || operation.MaxOutputBytes > 8<<20 {
			return Bundle{}, errors.New("invalid tool operation")
		}
		seen[operation.ID] = true
		for _, name := range operation.Env {
			if !envName.MatchString(name) || strings.HasPrefix(name, "TOS_TAG_") {
				return Bundle{}, fmt.Errorf("invalid environment name %q", name)
			}
		}
	}
	scriptBytes, err := readRootFile(absolute, manifest.Script, 1<<20)
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{Root: absolute, Manifest: manifest, ContentHash: contentHash(data, scriptBytes), ScriptHash: digest(scriptBytes)}, nil
}

func validApprovalPolicy(value string) bool {
	return value == ApprovalRiskBased || value == ApprovalAlways || value == ApprovalNever
}

func (e Executor) Execute(parent context.Context, bundle Bundle, request Request) (Result, error) {
	if !e.Enabled {
		return Result{}, errors.New("real tool execution is disabled")
	}
	operation, ok := findOperation(bundle.Manifest, request.OperationID)
	if !ok {
		return Result{}, errors.New("operation is not declared")
	}
	if operation.Risk == "admin" {
		return Result{}, errors.New("admin tool operations are disabled")
	}
	capability := request.Capability
	if capability.ToolID != bundle.Manifest.ID || capability.ToolVersion != bundle.Manifest.Version || capability.OperationID != operation.ID || capability.AttemptToken == "" || capability.SteeringEpoch <= 0 || !capability.ExpiresAt.After(time.Now().UTC()) {
		return Result{}, errors.New("capability does not authorize this execution")
	}
	if _, err := safeChild(bundle.Root, bundle.Manifest.Script); err != nil {
		return Result{}, err
	}
	scriptBytes, err := readRootFile(bundle.Root, bundle.Manifest.Script, 1<<20)
	if err != nil {
		return Result{}, fmt.Errorf("read reviewed tool script: %w", err)
	}
	if bundle.ScriptHash == "" || digest(scriptBytes) != bundle.ScriptHash {
		return Result{}, errors.New("reviewed tool script hash changed")
	}
	allowed := make(map[string]bool, len(operation.Env))
	toolPath := strings.TrimSpace(e.Path)
	if toolPath == "" {
		toolPath = "/usr/bin:/bin"
	}
	privateHome, err := os.MkdirTemp("", "tos-tag-tool-")
	if err != nil {
		return Result{}, fmt.Errorf("create private tool home: %w", err)
	}
	defer func() { _ = os.RemoveAll(privateHome) }()
	environment := []string{"PATH=" + toolPath, "LANG=C.UTF-8", "HOME=" + privateHome, "TMPDIR=" + privateHome, "TOS_TAG_OPERATION_ID=" + operation.ID}
	for _, name := range operation.Env {
		allowed[name] = true
		value, exists := request.SecretValues[name]
		if !exists {
			return Result{}, fmt.Errorf("missing secret binding %s", name)
		}
		environment = append(environment, name+"="+value)
	}
	for name := range request.SecretValues {
		if !allowed[name] {
			return Result{}, fmt.Errorf("undeclared environment binding %s", name)
		}
	}
	for _, argument := range request.Args {
		for _, secret := range request.SecretValues {
			if secret != "" && strings.Contains(argument, secret) {
				return Result{}, errors.New("secret values must be passed only through declared environment bindings")
			}
		}
	}
	ctx, cancel := context.WithTimeout(parent, time.Duration(operation.TimeoutSeconds)*time.Second)
	defer cancel()
	// Reviewed helpers may use Bash deliberately (the Agent Wiki and Linear
	// clients do). The script still arrives on stdin and typed arguments are
	// appended separately, so no model-provided command string is evaluated.
	command := exec.CommandContext(ctx, "/bin/bash", "-s", "--")
	// Append already-typed arguments to Cmd.Args; they are never parsed as a
	// command string and remain positional parameters to the reviewed script.
	command.Args = append(command.Args, request.Args...)
	command.Dir = bundle.Root
	command.Env = environment
	command.Stdin = bytes.NewReader(scriptBytes)
	limited := &limitedBuffer{remaining: operation.MaxOutputBytes}
	command.Stdout, command.Stderr = limited, limited
	started := time.Now()
	err = command.Run()
	result := Result{Output: redactSecrets(limited.String(), request.SecretValues), Duration: time.Since(started)}
	if exitErr := new(exec.ExitError); errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	}
	if ctx.Err() != nil {
		return result, fmt.Errorf("tool execution terminated: %w", ctx.Err())
	}
	if limited.exceeded {
		return result, errors.New("tool output exceeded limit")
	}
	if err != nil {
		return result, fmt.Errorf("tool failed with exit code %d", result.ExitCode)
	}
	if e.Usage != nil && request.OrganizationID != "" {
		_ = e.Usage.Record(parent, usage.Event{OrganizationID: request.OrganizationID, JobID: request.JobID, Category: "tool", Calls: 1, DurationMS: result.Duration.Milliseconds()})
	}
	return result, nil
}

func validRisk(value string) bool {
	switch value {
	case "read", "write", "destructive":
		return true
	default:
		return false
	}
}

func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func contentHash(values ...[]byte) string {
	hasher := sha256.New()
	for _, value := range values {
		_, _ = hasher.Write(value)
		_, _ = hasher.Write([]byte{0})
	}
	return "sha256:" + hex.EncodeToString(hasher.Sum(nil))
}

func findOperation(manifest Manifest, id string) (Operation, bool) {
	for _, operation := range manifest.Operations {
		if operation.ID == id {
			return operation, true
		}
	}
	return Operation{}, false
}

func safeChild(root, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) {
		return "", errors.New("path must be relative")
	}
	clean := filepath.Clean(relative)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("path traversal rejected")
	}
	path := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes bundle")
	}
	return path, nil
}

func readRootFile(root, relative string, limit int64) ([]byte, error) {
	scoped, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer scoped.Close()
	file, err := scoped.Open(relative)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("file exceeds size limit")
	}
	return data, nil
}

type limitedBuffer struct {
	buffer    bytes.Buffer
	remaining int
	exceeded  bool
}

func (b *limitedBuffer) Write(data []byte) (int, error) {
	original := len(data)
	if len(data) > b.remaining {
		data = data[:b.remaining]
		b.exceeded = true
	}
	_, _ = b.buffer.Write(data)
	b.remaining -= len(data)
	return original, nil
}

func (b *limitedBuffer) String() string { return b.buffer.String() }

func redactSecrets(value string, secrets map[string]string) string {
	for _, secret := range secrets {
		if secret != "" {
			value = strings.ReplaceAll(value, secret, "[REDACTED]")
		}
	}
	return value
}
