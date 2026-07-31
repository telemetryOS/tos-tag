package workers

import (
	"context"
	"sync"
	"time"

	"github.com/telemetryos/tos-tag/types"
)

type Fake struct {
	mu         sync.Mutex
	workspaces map[string]Workspace
}

func NewFake() *Fake { return &Fake{workspaces: make(map[string]Workspace)} }

func (f *Fake) Provision(_ context.Context, spec Spec) (Workspace, error) {
	if spec.JobID == "" || spec.AttemptID == "" {
		return Workspace{}, ErrUnsafeSpec
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now().UTC()
	workspace := Workspace{ID: types.NewID("worker"), OrganizationID: spec.OrganizationID, JobID: spec.JobID, AttemptID: spec.AttemptID, Root: "/fake", WorkDir: "/fake/work", SkillsDir: "/fake/skills", ArtifactsDir: "/fake/artifacts", CreatedAt: now, Deadline: now.Add(spec.WallTime)}
	f.workspaces[workspace.ID] = workspace
	return workspace, nil
}

func (f *Fake) ExportArtifacts(_ context.Context, workspace Workspace, _ []ArtifactSpec) ([]Artifact, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.workspaces[workspace.ID]; !ok {
		return nil, ErrNotFound
	}
	return nil, nil
}

func (f *Fake) Terminate(_ context.Context, workspace Workspace) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	current, ok := f.workspaces[workspace.ID]
	if !ok {
		return ErrNotFound
	}
	current.TerminatedAt = time.Now().UTC()
	f.workspaces[workspace.ID] = current
	return nil
}
