// Package workers owns disposable, secret-minimized worker workspaces.
package workers

import (
	"context"
	"errors"
	"time"

	"github.com/telemetryos/tos-tag/core/marketplace"
)

var (
	ErrNotFound   = errors.New("worker not found")
	ErrUnsafeSpec = errors.New("unsafe worker specification")
)

type Spec struct {
	OrganizationID string
	JobID          string
	AttemptID      string
	Command        []string
	Environment    map[string]string
	Provider       *ProviderRoute
	Skills         []marketplace.SkillSnapshot
	CustomTools    map[string][]byte
	WallTime       time.Duration
}

// ProviderRoute is a short-lived capability to a control-plane model gateway.
// Token is never an upstream provider credential.
type ProviderRoute struct {
	ID      string
	BaseURL string
	Token   string
}

type CapabilityRevoker interface {
	RevokeAttempt(context.Context, string) error
}

type Workspace struct {
	ID             string    `json:"id"`
	OrganizationID string    `json:"organization_id,omitempty"`
	JobID          string    `json:"job_id"`
	AttemptID      string    `json:"attempt_id"`
	Root           string    `json:"root"`
	WorkDir        string    `json:"work_dir"`
	SkillsDir      string    `json:"skills_dir"`
	ArtifactsDir   string    `json:"artifacts_dir"`
	PID            int       `json:"pid,omitempty"`
	CreatedAt      time.Time `json:"created_at"`
	Deadline       time.Time `json:"deadline"`
	TerminatedAt   time.Time `json:"terminated_at,omitempty"`
}

type ArtifactSpec struct {
	Path     string
	MaxBytes int64
}

type Artifact struct {
	Path string `json:"path"`
	Data []byte `json:"data"`
}

type Manager interface {
	Provision(context.Context, Spec) (Workspace, error)
	ExportArtifacts(context.Context, Workspace, []ArtifactSpec) ([]Artifact, error)
	Terminate(context.Context, Workspace) error
}
