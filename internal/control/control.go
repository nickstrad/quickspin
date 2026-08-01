// Package control holds the sandbox operations that make decisions: the create
// saga and its compensation, the destroy transition dance, and the running
// check every filesystem and exec route gates on. Pass-through reads stay with
// their callers — see docs/reference/package-boundaries.mdx.
package control

import (
	"context"
	"errors"
	"log/slog"

	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/nickstrad/quickspin/internal/sandbox"
	"github.com/nickstrad/quickspin/internal/store"
)

type Control struct {
	logger  *slog.Logger
	store   store.Store
	runtime runtime.Runtime
}

func New(logger *slog.Logger, store store.Store, runtime runtime.Runtime) *Control {
	return &Control{
		logger:  logger.With("subcomponent", "control"),
		store:   store,
		runtime: runtime,
	}
}

// CreateSandbox records the sandbox, starts it, and reports it running. A
// repeated idempotency key returns the original record untouched.
func (c *Control) CreateSandbox(ctx context.Context, idempotencyKey string, spec sandbox.SpecFile) (*sandbox.Sandbox, error) {
	const op = "control.Control.CreateSandbox"

	// Resolved before the store write so an unenforceable limit is a 422 with no
	// row behind it, rather than a pending sandbox that can never start.
	resolved, err := spec.Resolve()
	if err == nil {
		err = resolved.Validate()
	}
	if err != nil {
		return nil, Wrap(op, "resolving the submitted spec", err)
	}

	sbx, err := c.store.CreateSandbox(ctx, idempotencyKey, spec)
	if err != nil {
		// A store.ErrNotFound here is not the client's problem: it means the
		// idempotency key points at a sandbox that no longer exists, which is
		// our inconsistency, so only an invalid spec is allowed to be a 4xx.
		if !errors.Is(err, sandbox.ErrInvalidSpec) {
			err = errors.Join(ErrInternal, err)
		}
		return nil, Wrap(op, "recording the sandbox", err)
	}

	// A repeated key returns the original sandbox, which already has a runtime;
	// starting a second one is exactly the duplicate the key exists to prevent.
	if sbx.State != sandbox.Pending {
		c.logger.InfoContext(ctx, "returning the existing sandbox for a repeated idempotency key",
			"idempotencyKey", idempotencyKey, "sandboxID", sbx.SandboxID, "state", sbx.State)
		return sbx, nil
	}

	// The store minted the id and it is already durable, so the runtime is told
	// which sandbox it is building rather than inventing a second identity.
	if _, err := c.runtime.Create(ctx, sbx.SandboxID, resolved); err != nil {
		c.markFailed(ctx, sbx.SandboxID)
		return nil, Wrap(op, "starting the sandbox", err)
	}

	// Create starts the container, so success means running rather than
	// created-but-idle.
	running, err := c.store.UpdateSandboxState(ctx, sbx.SandboxID, sandbox.Pending, sandbox.Running)
	if err != nil {
		return nil, Wrap(op, "recording the sandbox as running", errors.Join(ErrInternal, err))
	}

	return running, nil
}

// markFailed records a create that the runtime refused. The error is logged
// rather than returned: the caller is already on its way to a 5xx, and a row
// left in pending is a reconciler's problem, not this request's.
func (c *Control) markFailed(ctx context.Context, sandboxID string) {
	if _, err := c.store.UpdateSandboxState(ctx, sandboxID, sandbox.Pending, sandbox.Failed); err != nil {
		c.logger.ErrorContext(ctx, "marking the sandbox failed after the runtime refused it",
			"sandboxID", sandboxID, "err", err)
		return
	}
	c.logger.WarnContext(ctx, "sandbox marked failed after the runtime refused it", "sandboxID", sandboxID)
}

// DestroySandbox is idempotent: a sandbox that is absent or already past
// running is the outcome the caller asked for, so it reports success.
func (c *Control) DestroySandbox(ctx context.Context, sandboxID string) error {
	const op = "control.Control.DestroySandbox"

	// Running is the only state Stopping is reachable from, so naming it beats
	// reading the row to feed its own state back in: the store's WHERE gate
	// rejects every other case and says whether the id was absent or the state
	// was wrong.
	if _, err := c.store.UpdateSandboxState(ctx, sandboxID, sandbox.Running, sandbox.Stopping); err != nil {
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

	if err := c.runtime.Destroy(ctx, sandboxID); err != nil {
		return Wrap(op, "destroying the sandbox", err)
	}

	// The container is gone by now, so a row still in stopping is our
	// inconsistency rather than anything the client did wrong.
	if _, err := c.store.UpdateSandboxState(ctx, sandboxID, sandbox.Stopping, sandbox.Stopped); err != nil {
		return Wrap(op, "recording the sandbox as stopped", errors.Join(ErrInternal, err))
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
