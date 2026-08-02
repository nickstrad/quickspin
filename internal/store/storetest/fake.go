package storetest

import (
	"context"
	"time"

	"github.com/nickstrad/quickspin/internal/events"
	"github.com/nickstrad/quickspin/internal/sandbox"
	"github.com/nickstrad/quickspin/internal/store"
)

// Fake answers each call from the matching field, so a test states the answer
// it needs — including an error — with no database involved.
//
// The embedded interface is nil on purpose. It satisfies store.Store without
// stub methods, so a test that only needs GetSandbox sets only GetSandboxFn,
// and a consumer that unexpectedly calls CreateSandbox panics instead of
// receiving a nil row. The tradeoff: adding a method to store.Store no longer
// breaks this file at compile time, it breaks callers at run time.
type Fake struct {
	store.Store

	GetIdempotencyKeyFn    func(context.Context, string) (*sandbox.IdempotencyKey, error)
	CreateIdempotencyKeyFn func(context.Context, string, string) (*sandbox.IdempotencyKey, error)
	CreateSandboxFn        func(context.Context, string, sandbox.SpecFile, time.Time) (*sandbox.Sandbox, error)
	GetSandboxFn           func(context.Context, string) (*sandbox.Sandbox, error)
	GetSandboxesFn         func(context.Context) ([]*sandbox.Sandbox, error)
	UpdateSandboxExpiryFn  func(context.Context, string, time.Time) (*sandbox.Sandbox, error)
	UpdateSandboxStateFn   func(context.Context, string, sandbox.TaskState, sandbox.TaskState, string) (*sandbox.Sandbox, error)
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

func (f Fake) UpdateSandboxState(ctx context.Context, sandboxID string, from, to sandbox.TaskState, reason string) (*sandbox.Sandbox, error) {
	return f.UpdateSandboxStateFn(ctx, sandboxID, from, to, reason)
}

func (f Fake) GetSandboxEvents(ctx context.Context, sandboxID string) ([]*events.Event, error) {
	return f.GetSandboxEventsFn(ctx, sandboxID)
}
