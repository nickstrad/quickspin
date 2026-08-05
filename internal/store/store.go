// Package store is the persistence contract for the domain types in
// internal/sandbox. It holds no SQL and no domain logic; implementations live
// in subpackages and are pinned by the storetest conformance suite.
package store

import (
	"context"
	"time"

	"github.com/nickstrad/quickspin/internal/events"
	"github.com/nickstrad/quickspin/internal/sandbox"
)

type Store interface {
	GetIdempotencyKey(ctx context.Context, idempotencyKey string) (*sandbox.IdempotencyKey, error)
	CreateIdempotencyKey(ctx context.Context, idempotencyKey, sandboxID string) (*sandbox.IdempotencyKey, error)
	// A zero expiresAt is rejected with ErrMissingExpiry: every sandbox row
	// carries the instant the reaper acts on, so none can be unreapable.
	CreateSandbox(ctx context.Context, idempotencyKey string, spec sandbox.SpecFile, expiresAt time.Time) (*sandbox.Sandbox, error)
	GetSandbox(ctx context.Context, sandboxID string) (*sandbox.Sandbox, error)
	GetSandboxes(ctx context.Context) ([]*sandbox.Sandbox, error)
	// UpdateSandboxExpiry renews only pending or running rows and rejects a
	// zero expiry.
	UpdateSandboxExpiry(ctx context.Context, sandboxID string, expiresAt time.Time) (*sandbox.Sandbox, error)
	// UpdateSandboxState requires the caller's observed version. A successful
	// transition increments it and atomically appends its event.
	UpdateSandboxState(ctx context.Context, sandboxID string, from, to sandbox.TaskState, reason string, versionID int) (*sandbox.Sandbox, error)
	GetSandboxEvents(ctx context.Context, sandboxID string) ([]*events.Event, error)
}
