// Package control holds the sandbox operations that make decisions: recording
// create and destroy intent for the reconciler to converge, and the running
// check every filesystem and exec route gates on. Pass-through reads stay with
// their callers — see docs/reference/package-boundaries.mdx.
package control

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/nickstrad/quickspin/internal/sandbox"
	"github.com/nickstrad/quickspin/internal/store"
)

type Control struct {
	logger  *slog.Logger
	store   store.Store
	runtime runtime.Runtime
	// Injected so TTL tests need not sleep.
	now func() time.Time
}

func New(logger *slog.Logger, store store.Store, runtime runtime.Runtime) *Control {
	return &Control{
		logger:  logger.With("subcomponent", "control"),
		store:   store,
		runtime: runtime,
		now:     time.Now,
	}
}

// CreateSandbox records the sandbox as pending and returns; the reconciler is
// what starts it and moves it to running. A repeated idempotency key returns
// the original record untouched. A zero ttl takes sandbox.DefaultTTL.
func (c *Control) CreateSandbox(ctx context.Context, idempotencyKey string, spec sandbox.SpecFile, ttl time.Duration) (*sandbox.Sandbox, error) {
	const op = "control.Control.CreateSandbox"

	// Resolved before the store write so an unenforceable limit is a 422 with no
	// row behind it, rather than a pending sandbox that can never start.
	if _, err := spec.ResolveValidated(); err != nil {
		return nil, Wrap(op, "resolving the submitted spec", err)
	}

	ttl, err := sandbox.ResolveTTL(ttl)
	if err != nil {
		return nil, Wrap(op, "resolving the requested ttl", err)
	}

	sbx, err := c.store.CreateSandbox(ctx, idempotencyKey, spec, c.now().Add(ttl))
	if err != nil {
		// A store.ErrNotFound here is not the client's problem: it means the
		// idempotency key points at a sandbox that no longer exists, which is
		// our inconsistency, so only an invalid spec is allowed to be a 4xx.
		if !errors.Is(err, sandbox.ErrInvalidSpec) {
			err = errors.Join(ErrInternal, err)
		}
		return nil, Wrap(op, "recording the sandbox", err)
	}

	return sbx, nil
}

// KeepaliveSandbox replaces the lease with one measured from the current time.
func (c *Control) KeepaliveSandbox(ctx context.Context, sandboxID string, ttl time.Duration) (*sandbox.Sandbox, error) {
	const op = "control.Control.KeepaliveSandbox"

	ttl = min(ttl, sandbox.MaxTTL)
	ttl, err := sandbox.ResolveTTL(ttl)
	if err != nil {
		return nil, Wrap(op, "resolving the requested ttl", err)
	}

	expiresAt := c.now().Add(ttl)
	sbx, err := c.store.UpdateSandboxExpiry(ctx, sandboxID, expiresAt)
	if err != nil {
		return nil, Wrap(op, "renewing the sandbox expiry", err)
	}

	return sbx, nil
}

// DestroySandbox records the sandbox as stopping and returns; the reconciler is
// what removes the container and moves it to stopped. It is idempotent: a
// sandbox that is absent or already past running is the outcome the caller
// asked for, so it reports success.
func (c *Control) DestroySandbox(ctx context.Context, sandboxID string) error {
	const op = "control.Control.DestroySandbox"

	// Running is the only state Stopping is reachable from, so naming it beats
	// reading the row to feed its own state back in: the store's WHERE gate
	// rejects every other case and says whether the id was absent or the state
	// was wrong.
	if _, err := c.store.UpdateSandboxState(ctx, sandboxID, sandbox.Running, sandbox.Stopping, "destroy requested"); err != nil {
		// An absent row or one already past Running means the sandbox is not
		// running, which is the outcome the caller asked for — DELETE is
		// idempotent, so that is a success, not an error.
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, sandbox.ErrInvalidStateTransition) {
			c.logger.InfoContext(ctx, "destroy found nothing to stop",
				"sandboxID", sandboxID, "err", err)
			return nil
		}
		return Wrap(op, "marking the sandbox stopping", err)
	}

	return nil
}

// RequireRunning reports whether the sandbox is in a state that allows work to
// be done inside it. The sentinel it returns is what the caller classifies:
// not running, an unknown id, or a store failure.
func (c *Control) RequireRunning(ctx context.Context, sandboxID string) error {
	const op = "control.Control.RequireRunning"

	sbx, err := c.store.GetSandbox(ctx, sandboxID)
	if err != nil {
		return Wrap(op, "loading the sandbox", err)
	}

	if sbx.State != sandbox.Running {
		return Wrap(op, "", sandbox.ErrSandboxNotRunning)
	}
	return nil
}
