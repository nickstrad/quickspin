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
	// reason is recorded on the event this transition appends; the row and its
	// event commit together or not at all.
	UpdateSandboxState(ctx context.Context, sandboxID string, from, to sandbox.TaskState, reason string) (*sandbox.Sandbox, error)
	GetSandboxEvents(ctx context.Context, sandboxID string) ([]*events.Event, error)
}
