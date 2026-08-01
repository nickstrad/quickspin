// Package store is the persistence contract for the domain types in
// internal/sandbox. It holds no SQL and no domain logic; implementations live
// in subpackages and are pinned by the storetest conformance suite.
package store

import (
	"context"

	"github.com/nickstrad/quickspin/internal/sandbox"
)

type Store interface {
	GetIdempotencyKey(ctx context.Context, idempotencyKey string) (*sandbox.IdempotencyKey, error)
	CreateIdempotencyKey(ctx context.Context, idempotencyKey, sandboxID string) (*sandbox.IdempotencyKey, error)
	CreateSandbox(ctx context.Context, idempotencyKey string, spec sandbox.SpecFile) (*sandbox.Sandbox, error)
	GetSandbox(ctx context.Context, sandboxID string) (*sandbox.Sandbox, error)
	GetSandboxes(ctx context.Context) ([]*sandbox.Sandbox, error)
	UpdateSandboxState(ctx context.Context, sandboxID string, from, to sandbox.TaskState) (*sandbox.Sandbox, error)
}
