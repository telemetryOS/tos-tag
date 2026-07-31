package tools

import (
	"context"
	"errors"

	"github.com/telemetryos/tos-tag/core/keystore"
)

// Gateway resolves opaque secret references only after the reviewed manifest
// and attempt capability have been selected. Plaintext values exist only in the
// exact tool subprocess environment assembled by Executor.
type Gateway struct {
	Registry *Registry
	Secrets  keystore.Repository
	Executor Executor
}

type GatewayRequest struct {
	Request
	SecretReferences map[string]string
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
	allowed := make(map[string]bool, len(operation.Env))
	for _, name := range operation.Env {
		allowed[name] = true
	}
	for name := range request.SecretReferences {
		if !allowed[name] {
			return Result{}, errors.New("secret reference is not declared by the operation")
		}
	}
	request.SecretValues = make(map[string]string, len(request.SecretReferences))
	for name, referenceID := range request.SecretReferences {
		value, err := g.Secrets.Resolve(ctx, request.OrganizationID, referenceID)
		if err != nil {
			return Result{}, err
		}
		request.SecretValues[name] = value
	}
	return g.Executor.Execute(ctx, bundle, request.Request)
}
