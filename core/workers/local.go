package workers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/telemetryos/tos-tag/core/marketplace"
	"github.com/telemetryos/tos-tag/core/usage"
	"github.com/telemetryos/tos-tag/types"
)

var safeEnv = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)

type localProcess struct {
	workspace Workspace
	command   *exec.Cmd
	cancel    context.CancelFunc
	done      chan error
	finished  chan struct{}
}

type Local struct {
	mu      sync.Mutex
	baseDir string
	path    string
	active  map[string]*localProcess
	revoker CapabilityRevoker
	usage   usage.Recorder
}

func NewLocal(baseDir, path string) (*Local, error) {
	if baseDir == "" {
		return nil, fmt.Errorf("worker base directory is required")
	}
	absolute, err := filepath.Abs(baseDir)
	if err != nil {
		return nil, err
	}
	if path == "" {
		path = "/usr/local/bin:/usr/bin:/bin"
	}
	return &Local{baseDir: absolute, path: path, active: make(map[string]*localProcess)}, nil
}
func NewLocalWithDependencies(baseDir, path string, revoker CapabilityRevoker, recorder usage.Recorder) (*Local, error) {
	manager, err := NewLocal(baseDir, path)
	if err != nil {
		return nil, err
	}
	manager.revoker = revoker
	manager.usage = recorder
	return manager, nil
}

func (m *Local) Provision(parent context.Context, spec Spec) (Workspace, error) {
	if spec.JobID == "" || spec.AttemptID == "" || len(spec.Command) == 0 || spec.Command[0] == "" || spec.WallTime <= 0 {
		return Workspace{}, ErrUnsafeSpec
	}
	for name := range spec.Environment {
		if !safeEnv.MatchString(name) || isForbiddenEnvironment(name) {
			return Workspace{}, fmt.Errorf("%w: environment %s is not worker-safe", ErrUnsafeSpec, name)
		}
	}
	if err := os.MkdirAll(m.baseDir, 0o700); err != nil {
		return Workspace{}, err
	}
	root, err := os.MkdirTemp(m.baseDir, "worker-")
	if err != nil {
		return Workspace{}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = cleanupWorkerRoot(root)
		}
	}()
	workDir := filepath.Join(root, "work")
	skillsDir := filepath.Join(workDir, ".opencode", "skills")
	toolsDir := filepath.Join(workDir, ".opencode", "tools")
	artifactsDir, xdgDir, tempDir := filepath.Join(root, "artifacts"), filepath.Join(root, "xdg"), filepath.Join(root, "tmp")
	for _, directory := range []string{workDir, skillsDir, toolsDir, artifactsDir, xdgDir, tempDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			return Workspace{}, err
		}
	}
	if err := writeWorkerPolicy(workDir, len(spec.CustomTools) > 0); err != nil {
		return Workspace{}, err
	}
	if err := materializeSkills(spec.Skills, skillsDir); err != nil {
		return Workspace{}, err
	}
	if err := materializeCustomTools(spec.CustomTools, toolsDir); err != nil {
		return Workspace{}, err
	}
	now := time.Now().UTC()
	ctx, cancel := context.WithTimeout(parent, spec.WallTime)
	executable, err := exec.LookPath(spec.Command[0])
	if err != nil {
		cancel()
		return Workspace{}, err
	}
	command := &exec.Cmd{Path: executable, Args: append([]string{executable}, spec.Command[1:]...)}
	command.Dir = workDir
	command.Env = []string{"PATH=" + m.path, "LANG=C.UTF-8", "HOME=" + root, "XDG_DATA_HOME=" + xdgDir, "XDG_CONFIG_HOME=" + xdgDir, "XDG_CACHE_HOME=" + xdgDir, "TMPDIR=" + tempDir, "TOS_TAG_SKILLS=" + skillsDir, "TOS_TAG_ARTIFACTS=" + artifactsDir}
	for name, value := range spec.Environment {
		command.Env = append(command.Env, name+"="+value)
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Stdout, command.Stderr = io.Discard, io.Discard
	if err := command.Start(); err != nil {
		cancel()
		return Workspace{}, err
	}
	workspace := Workspace{ID: types.NewID("worker"), OrganizationID: spec.OrganizationID, JobID: spec.JobID, AttemptID: spec.AttemptID, Root: root, WorkDir: workDir, SkillsDir: skillsDir, ArtifactsDir: artifactsDir, PID: command.Process.Pid, CreatedAt: now, Deadline: now.Add(spec.WallTime)}
	process := &localProcess{workspace: workspace, command: command, cancel: cancel, done: make(chan error, 1), finished: make(chan struct{})}
	m.mu.Lock()
	m.active[workspace.ID] = process
	m.mu.Unlock()
	go func() {
		process.done <- command.Wait()
		close(process.done)
		close(process.finished)
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		case <-process.finished:
		}
	}()
	cleanup = false
	if m.usage != nil && spec.OrganizationID != "" {
		_ = m.usage.Record(parent, usage.Event{OrganizationID: spec.OrganizationID, JobID: spec.JobID, Category: "worker_provision", Calls: 1})
	}
	return workspace, nil
}

func writeWorkerPolicy(workDir string, toolEnabled bool) error {
	root, err := os.OpenRoot(workDir)
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := root.OpenFile("opencode.json", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
	if err != nil {
		return err
	}
	defer file.Close()
	// The Slack-response worker is read-only by construction. Behavioral skills
	// may be loaded, but built-in shell, file mutation, web, task, and external
	// directory tools remain denied until a server-side capability gateway is
	// explicitly wired for a job.
	policy := `{"permission":{"*":"deny","skill":"allow"}}`
	if toolEnabled {
		policy = `{"permission":{"*":"deny","skill":"allow","tos_tag_tool":"allow"}}`
	}
	_, err = file.Write([]byte(policy))
	return err
}

func materializeCustomTools(custom map[string][]byte, target string) error {
	root, err := os.OpenRoot(target)
	if err != nil {
		return err
	}
	defer root.Close()
	for name, source := range custom {
		if !regexp.MustCompile(`^[a-z][a-z0-9_-]*\.ts$`).MatchString(name) || len(source) == 0 || len(source) > 1<<20 {
			return ErrUnsafeSpec
		}
		file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
		if err != nil {
			return err
		}
		_, writeErr := file.Write(source)
		closeErr := file.Close()
		if writeErr != nil {
			return writeErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func (m *Local) ExportArtifacts(ctx context.Context, workspace Workspace, specs []ArtifactSpec) ([]Artifact, error) {
	if err := m.owns(workspace); err != nil {
		return nil, err
	}
	result := make([]Artifact, 0, len(specs))
	root, err := os.OpenRoot(workspace.ArtifactsDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	for _, spec := range specs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if spec.Path == "" || filepath.IsAbs(spec.Path) || spec.MaxBytes <= 0 || strings.HasPrefix(filepath.Clean(spec.Path), "..") {
			return nil, ErrUnsafeSpec
		}
		file, err := root.Open(spec.Path)
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, spec.MaxBytes+1))
		_ = file.Close()
		if readErr != nil {
			return nil, readErr
		}
		if int64(len(data)) > spec.MaxBytes {
			return nil, fmt.Errorf("artifact %s exceeds limit", spec.Path)
		}
		result = append(result, Artifact{Path: filepath.ToSlash(filepath.Clean(spec.Path)), Data: data})
	}
	return result, nil
}

func (m *Local) Terminate(ctx context.Context, workspace Workspace) error {
	m.mu.Lock()
	process, ok := m.active[workspace.ID]
	if ok {
		delete(m.active, workspace.ID)
	}
	m.mu.Unlock()
	if !ok || process.workspace.Root != workspace.Root || process.workspace.AttemptID != workspace.AttemptID {
		return ErrNotFound
	}
	var revokeErr error
	if m.revoker != nil {
		revokeErr = m.revoker.RevokeAttempt(ctx, workspace.AttemptID)
	}
	process.cancel()
	_ = syscall.Kill(-process.command.Process.Pid, syscall.SIGTERM)
	select {
	case <-process.done:
	case <-time.After(500 * time.Millisecond):
		_ = syscall.Kill(-process.command.Process.Pid, syscall.SIGKILL)
		select {
		case <-process.done:
		case <-ctx.Done():
			return ctx.Err()
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	cleanupErr := cleanupWorkerRoot(workspace.Root)
	if m.usage != nil && workspace.OrganizationID != "" {
		_ = m.usage.Record(context.Background(), usage.Event{OrganizationID: workspace.OrganizationID, JobID: workspace.JobID, Category: "worker_terminate", Calls: 1})
	}
	if revokeErr != nil {
		return revokeErr
	}
	return cleanupErr
}

func (m *Local) owns(workspace Workspace) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	process, ok := m.active[workspace.ID]
	if !ok || process.workspace.Root != workspace.Root || process.workspace.AttemptID != workspace.AttemptID {
		return ErrNotFound
	}
	return nil
}

func materializeSkills(snapshots []marketplace.SkillSnapshot, target string) error {
	for _, snapshot := range snapshots {
		if snapshot.Name == "" || snapshot.Root == "" {
			return ErrUnsafeSpec
		}
		destination := filepath.Join(target, snapshot.Name)
		rel, relErr := filepath.Rel(target, destination)
		if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return ErrUnsafeSpec
		}
		if err := os.MkdirAll(destination, 0o700); err != nil {
			return err
		}
		sourceRoot, err := os.OpenRoot(snapshot.Root)
		if err != nil {
			return err
		}
		destinationRoot, err := os.OpenRoot(destination)
		if err != nil {
			_ = sourceRoot.Close()
			return err
		}
		hash := sha256.New()
		for _, relative := range snapshot.Files {
			if relative == "" || filepath.IsAbs(relative) || strings.HasPrefix(filepath.Clean(relative), "..") {
				_ = destinationRoot.Close()
				_ = sourceRoot.Close()
				return ErrUnsafeSpec
			}
			input, err := sourceRoot.Open(relative)
			if err != nil {
				_ = destinationRoot.Close()
				_ = sourceRoot.Close()
				return err
			}
			cleanRelative := filepath.Clean(relative)
			if err := destinationRoot.MkdirAll(filepath.Dir(cleanRelative), 0o700); err != nil {
				_ = input.Close()
				_ = destinationRoot.Close()
				_ = sourceRoot.Close()
				return err
			}
			output, err := destinationRoot.OpenFile(cleanRelative, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o400)
			if err != nil {
				_ = input.Close()
				_ = destinationRoot.Close()
				_ = sourceRoot.Close()
				return err
			}
			data, readErr := io.ReadAll(io.LimitReader(input, (4<<20)+1))
			if readErr != nil || len(data) > 4<<20 {
				_ = output.Close()
				_ = input.Close()
				_ = destinationRoot.Close()
				_ = sourceRoot.Close()
				return ErrUnsafeSpec
			}
			_, copyErr := output.Write(data)
			_, _ = hash.Write([]byte(filepath.ToSlash(relative)))
			_, _ = hash.Write([]byte{0})
			_, _ = hash.Write(data)
			_, _ = hash.Write([]byte{0})
			_ = output.Close()
			_ = input.Close()
			if copyErr != nil {
				_ = destinationRoot.Close()
				_ = sourceRoot.Close()
				return copyErr
			}
		}
		_ = sourceRoot.Close()
		_ = destinationRoot.Close()
		if snapshot.Hash != "sha256:"+hex.EncodeToString(hash.Sum(nil)) {
			return fmt.Errorf("%w: skill snapshot hash changed", ErrUnsafeSpec)
		}
		permissionRoot, err := os.OpenRoot(destination)
		if err != nil {
			return err
		}
		if err := filepath.WalkDir(destination, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			relative, err := filepath.Rel(destination, path)
			if err != nil {
				return err
			}
			if entry.IsDir() {
				return permissionRoot.Chmod(relative, 0o500)
			}
			return permissionRoot.Chmod(relative, 0o400)
		}); err != nil {
			_ = permissionRoot.Close()
			return err
		}
		_ = permissionRoot.Close()
	}
	return nil
}

func isForbiddenEnvironment(name string) bool {
	for _, fragment := range []string{"TOKEN", "SECRET", "PASSWORD", "KEY", "SLACK", "MONGO", "AWS", "GITHUB", "LINEAR"} {
		if strings.Contains(name, fragment) {
			return true
		}
	}
	return false
}

func cleanupWorkerRoot(root string) error {
	permissionRoot, openErr := os.OpenRoot(root)
	if openErr != nil {
		return os.RemoveAll(root)
	}
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		if entry.IsDir() {
			_ = permissionRoot.Chmod(relative, 0o700)
		} else {
			_ = permissionRoot.Chmod(relative, 0o600)
		}
		return nil
	})
	_ = permissionRoot.Close()
	return os.RemoveAll(root)
}
