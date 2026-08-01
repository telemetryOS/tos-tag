package tools

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/telemetryos/tos-tag/core/keystore"
)

// Gateway resolves opaque secret references only after the reviewed manifest
// and attempt capability have been selected. Plaintext values exist only in the
// exact tool subprocess environment assembled by Executor.
type Gateway struct {
	Registry *Registry
	Secrets  keystore.Repository
	Executor Executor
	// Bindings contains only opaque keystore references selected by the control
	// plane. It is never supplied by, or serialized to, the model worker.
	Bindings map[string]map[string]string
}

type GatewayRequest struct {
	Request
}

func (g Gateway) Execute(ctx context.Context, toolID string, request GatewayRequest) (Result, error) {
	if g.Registry == nil || g.Secrets == nil {
		return Result{}, errors.New("tool gateway is not configured")
	}
	bundle, ok := g.Registry.Resolve(toolID)
	if !ok {
		return Result{}, errors.New("tool is not installed")
	}
	operation, ok := findOperation(bundle.Manifest, request.OperationID)
	if !ok {
		return Result{}, errors.New("operation is not declared")
	}
	organizationBindings := g.Bindings[request.OrganizationID]
	request.SecretValues = make(map[string]string, len(operation.Env))
	for _, name := range operation.Env {
		referenceID := organizationBindings[name]
		if referenceID == "" {
			return Result{}, fmt.Errorf("required environment binding %s is not configured", name)
		}
		value, err := g.Secrets.Resolve(ctx, request.OrganizationID, referenceID)
		if err != nil {
			return Result{}, err
		}
		request.SecretValues[name] = value
	}
	return g.Executor.Execute(ctx, bundle, request.Request)
}

// ImportEnvironmentBindings copies only the environment variables declared by
// the selected reviewed tools into the encrypted keystore. The returned map
// contains opaque references, never plaintext values.
func ImportEnvironmentBindings(ctx context.Context, registry *Registry, toolIDs []string, secrets keystore.Repository, organizationID string, lookup func(string) (string, bool)) (map[string]map[string]string, error) {
	if registry == nil || secrets == nil || strings.TrimSpace(organizationID) == "" {
		return nil, errors.New("tool environment import requires registry, keystore, and organization")
	}
	if lookup == nil {
		lookup = os.LookupEnv
	}
	requiredBy := make(map[string][]string)
	for _, toolID := range toolIDs {
		bundle, ok := registry.Resolve(toolID)
		if !ok {
			return nil, fmt.Errorf("injected tool %q is not installed", toolID)
		}
		seen := make(map[string]bool)
		for _, operation := range bundle.Manifest.Operations {
			for _, name := range operation.Env {
				if !seen[name] {
					requiredBy[name] = append(requiredBy[name], toolID)
					seen[name] = true
				}
			}
		}
	}
	names := make([]string, 0, len(requiredBy))
	for name := range requiredBy {
		names = append(names, name)
	}
	sort.Strings(names)
	bindings := map[string]map[string]string{organizationID: {}}
	for _, name := range names {
		value, ok := lookup(name)
		if !ok || strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("required tool environment %s is not configured", name)
		}
		reference, err := secrets.Put(ctx, organizationID, name, "reviewed tools: "+strings.Join(requiredBy[name], ","), value)
		if err != nil {
			return nil, fmt.Errorf("store tool environment %s: %w", name, err)
		}
		bindings[organizationID][name] = reference.ID
	}
	return bindings, nil
}
