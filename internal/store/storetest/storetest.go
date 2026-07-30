package storetest

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/nickstrad/quickspin/internal/store"
)

type Factory func(t *testing.T) store.Store

func Run(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("CreatePersistsPendingSandbox", func(t *testing.T) {
		ctx := context.Background()
		st := factory(t)

		created := createSandbox(t, ctx, st, "create-and-get", `{"image":"alpine:3.20"}`)
		if created.ID == 0 {
			t.Error("CreateSandbox ID = 0, want a persisted ID")
		}
		if created.State != store.Pending {
			t.Errorf("CreateSandbox State = %q, want %q", created.State, store.Pending)
		}
		if created.CreatedAt.IsZero() || created.UpdatedAt.IsZero() {
			t.Errorf(
				"CreateSandbox timestamps = (%v, %v), want both populated",
				created.CreatedAt,
				created.UpdatedAt,
			)
		}

		got, err := st.GetSandbox(ctx, strconv.Itoa(created.ID))
		if err != nil {
			t.Fatalf("GetSandbox(%d) error = %v, want nil", created.ID, err)
		}
		if got.ID != created.ID || got.State != created.State {
			t.Errorf("GetSandbox(%d) = %#v, want persisted identity and state from %#v", created.ID, got, created)
		}
		if got.Spec.Image == nil || *got.Spec.Image != "alpine:3.20" {
			t.Errorf("GetSandbox(%d) Spec.Image = %v, want alpine:3.20", created.ID, got.Spec.Image)
		}
	})

	t.Run("MissingSandboxReturnsNotFound", func(t *testing.T) {
		ctx := context.Background()
		st := factory(t)

		got, err := st.GetSandbox(ctx, "999999")
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("GetSandbox(missing) error = %v, want ErrNotFound", err)
		}
		if got != nil {
			t.Errorf("GetSandbox(missing) = %#v, want nil", got)
		}
	})

	t.Run("IdempotencyKeyReturnsSameSandbox", func(t *testing.T) {
		ctx := context.Background()
		st := factory(t)

		first := createSandbox(t, ctx, st, "same-operation", `{"image":"alpine:3.20"}`)
		second := createSandbox(t, ctx, st, "same-operation", `{"image":"debian:12"}`)

		if second.ID != first.ID {
			t.Errorf("retry sandbox ID = %d, want original ID %d", second.ID, first.ID)
		}
		if second.Spec.Image == nil || *second.Spec.Image != "alpine:3.20" {
			t.Errorf("retry Spec.Image = %v, want original request's alpine:3.20", second.Spec.Image)
		}

		key, err := st.GetIdempotencyKey(ctx, "same-operation")
		if err != nil {
			t.Fatalf("GetIdempotencyKey() error = %v, want nil", err)
		}
		if key == nil || key.SandboxID != strconv.Itoa(first.ID) {
			t.Errorf("GetIdempotencyKey() = %#v, want sandbox ID %d", key, first.ID)
		}
	})

	t.Run("StoreReturnsCopies", func(t *testing.T) {
		ctx := context.Background()
		st := factory(t)

		first := createSandbox(t, ctx, st, "copy-semantics", `{"image":"alpine:3.20","env":{"MODE":"test"}}`)
		first.State = store.Failed
		first.Spec.Env["MODE"] = "mutated"

		got, err := st.GetSandbox(ctx, strconv.Itoa(first.ID))
		if err != nil {
			t.Fatalf("GetSandbox(%d) error = %v, want nil", first.ID, err)
		}
		if got.State != store.Pending {
			t.Errorf("re-read State = %q, want %q", got.State, store.Pending)
		}
		if got.Spec.Env["MODE"] != "test" {
			t.Errorf("re-read Env[MODE] = %q, want test", got.Spec.Env["MODE"])
		}
	})

	t.Run("IllegalTransitionRejected", func(t *testing.T) {
		ctx := context.Background()
		st := factory(t)
		sandbox := createSandbox(t, ctx, st, "illegal-transition", `{"image":"alpine:3.20"}`)
		id := strconv.Itoa(sandbox.ID)

		transition(t, ctx, st, id, store.Pending, store.Running)
		transition(t, ctx, st, id, store.Running, store.Stopping)
		transition(t, ctx, st, id, store.Stopping, store.Stopped)

		if _, err := st.UpdateSandboxState(ctx, string(store.Stopped), string(store.Running), id); !errors.Is(err, store.ErrInvalidStateTransition) {
			t.Fatalf("UpdateSandboxState(stopped, running) error = %v, want ErrInvalidStateTransition", err)
		}

		got, err := st.GetSandbox(ctx, id)
		if err != nil {
			t.Fatalf("GetSandbox(%s) error = %v, want nil", id, err)
		}
		if got.State != store.Stopped {
			t.Errorf("State after rejected transition = %q, want %q", got.State, store.Stopped)
		}
	})

	t.Run("TransitionRequiresCurrentFromState", func(t *testing.T) {
		ctx := context.Background()
		st := factory(t)
		sandbox := createSandbox(t, ctx, st, "stale-transition", `{"image":"alpine:3.20"}`)
		id := strconv.Itoa(sandbox.ID)

		if _, err := st.UpdateSandboxState(ctx, string(store.Running), string(store.Stopping), id); !errors.Is(err, store.ErrInvalidStateTransition) {
			t.Fatalf(
				"UpdateSandboxState(running, stopping) for a pending row error = %v, want ErrInvalidStateTransition",
				err,
			)
		}

		got, err := st.GetSandbox(ctx, id)
		if err != nil {
			t.Fatalf("GetSandbox(%s) error = %v, want nil", id, err)
		}
		if got.State != store.Pending {
			t.Errorf("State after stale transition = %q, want %q", got.State, store.Pending)
		}
	})
}

func createSandbox(
	t *testing.T,
	ctx context.Context,
	st store.Store,
	key string,
	spec string,
) *store.Sandbox {
	t.Helper()

	sandbox, err := st.CreateSandbox(ctx, key, spec)
	if err != nil {
		t.Fatalf("CreateSandbox(%q) error = %v, want nil", key, err)
	}
	return sandbox
}

func transition(
	t *testing.T,
	ctx context.Context,
	st store.Store,
	id string,
	from store.TaskState,
	to store.TaskState,
) *store.Sandbox {
	t.Helper()

	sandbox, err := st.UpdateSandboxState(ctx, string(from), string(to), id)
	if err != nil {
		t.Fatalf("UpdateSandboxState(%q, %q, %q) error = %v, want nil", from, to, id, err)
	}
	if sandbox.State != to {
		t.Fatalf("UpdateSandboxState(%q, %q, %q) State = %q, want %q", from, to, id, sandbox.State, to)
	}
	return sandbox
}
