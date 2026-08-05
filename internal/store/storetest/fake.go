package storetest

import (
	"context"
	"time"

	"github.com/nickstrad/quickspin/internal/events"
	"github.com/nickstrad/quickspin/internal/sandbox"
	"github.com/nickstrad/quickspin/internal/store"
)

// Fake delegates configured calls. Its nil embedded Store keeps tests focused:
// any unexpected or unconfigured call panics instead of returning a zero value.
type Fake struct {
	store.Store

	GetIdempotencyKeyFn    func(context.Context, string) (*sandbox.IdempotencyKey, error)
	CreateIdempotencyKeyFn func(context.Context, string, string) (*sandbox.IdempotencyKey, error)
	CreateSandboxFn        func(context.Context, string, sandbox.SpecFile, time.Time) (*sandbox.Sandbox, error)
	GetSandboxFn           func(context.Context, string) (*sandbox.Sandbox, error)
	GetSandboxesFn         func(context.Context) ([]*sandbox.Sandbox, error)
	UpdateSandboxExpiryFn  func(context.Context, string, time.Time) (*sandbox.Sandbox, error)
	UpdateSandboxStateFn   func(context.Context, string, sandbox.TaskState, sandbox.TaskState, string, int) (*sandbox.Sandbox, error)
	GetSandboxEventsFn     func(context.Context, string) ([]*events.Event, error)
}

var _ store.Store = Fake{}

func (f Fake) GetIdempotencyKey(ctx context.Context, idempotencyKey string) (*sandbox.IdempotencyKey, error) {
	return f.GetIdempotencyKeyFn(ctx, idempotencyKey)
}

func (f Fake) CreateIdempotencyKey(ctx context.Context, idempotencyKey, sandboxID string) (*sandbox.IdempotencyKey, error) {
	return f.CreateIdempotencyKeyFn(ctx, idempotencyKey, sandboxID)
}

func (f Fake) CreateSandbox(ctx context.Context, idempotencyKey string, spec sandbox.SpecFile, expiresAt time.Time) (*sandbox.Sandbox, error) {
	return f.CreateSandboxFn(ctx, idempotencyKey, spec, expiresAt)
}

func (f Fake) GetSandbox(ctx context.Context, sandboxID string) (*sandbox.Sandbox, error) {
	return f.GetSandboxFn(ctx, sandboxID)
}

func (f Fake) GetSandboxes(ctx context.Context) ([]*sandbox.Sandbox, error) {
	return f.GetSandboxesFn(ctx)
}

func (f Fake) UpdateSandboxExpiry(ctx context.Context, sandboxID string, expiresAt time.Time) (*sandbox.Sandbox, error) {
	return f.UpdateSandboxExpiryFn(ctx, sandboxID, expiresAt)
}

func (f Fake) UpdateSandboxState(ctx context.Context, sandboxID string, from, to sandbox.TaskState, reason string, versionID int) (*sandbox.Sandbox, error) {
	return f.UpdateSandboxStateFn(ctx, sandboxID, from, to, reason, versionID)
}

func (f Fake) GetSandboxEvents(ctx context.Context, sandboxID string) ([]*events.Event, error) {
	return f.GetSandboxEventsFn(ctx, sandboxID)
}
