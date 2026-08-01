package storetest

import (
	"context"
	"errors"
	"testing"

	"github.com/nickstrad/quickspin/internal/store"
)

type Factory func(t *testing.T) store.Store

func Run(t *testing.T, factory Factory) {
	t.Helper()

	t.Run("CreatePersistsPendingSandbox", func(t *testing.T) {
		ctx := context.Background()
		st := factory(t)

		created := createSandbox(t, ctx, st, "create-and-get", specFor("alpine:3.20"))
		if created.SandboxID == "" {
			t.Error("CreateSandbox SandboxID = \"\", want a minted id")
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

		got, err := st.GetSandbox(ctx, created.SandboxID)
		if err != nil {
			t.Fatalf("GetSandbox(%s) error = %v, want nil", created.SandboxID, err)
		}
		if got.SandboxID != created.SandboxID || got.State != created.State {
			t.Errorf("GetSandbox(%s) = %#v, want persisted identity and state from %#v", created.SandboxID, got, created)
		}
		if got.Spec.Image == nil || *got.Spec.Image != "alpine:3.20" {
			t.Errorf("GetSandbox(%s) Spec.Image = %v, want alpine:3.20", created.SandboxID, got.Spec.Image)
		}
	})

	// Stored specs preserve omitted fields rather than materializing defaults.
	t.Run("EmptySpecIsStoredUnresolved", func(t *testing.T) {
		ctx := context.Background()
		st := factory(t)

		created := createSandbox(t, ctx, st, "empty-spec", store.SpecFile{})

		got, err := st.GetSandbox(ctx, created.SandboxID)
		if err != nil {
			t.Fatalf("GetSandbox(%s) error = %v, want nil", created.SandboxID, err)
		}
		if got.Spec.Image != nil {
			t.Errorf("GetSandbox(%s) Spec.Image = %v, want nil", created.SandboxID, got.Spec.Image)
		}
	})

	t.Run("MissingSandboxReturnsNotFound", func(t *testing.T) {
		ctx := context.Background()
		st := factory(t)

		got, err := st.GetSandbox(ctx, "sbx_00000000-0000-0000-0000-000000000000")
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("GetSandbox(missing) error = %v, want ErrNotFound", err)
		}
		if got != nil {
			t.Errorf("GetSandbox(missing) = %#v, want nil", got)
		}
	})

	t.Run("ListReturnsEmptySliceWhenStoreIsEmpty", func(t *testing.T) {
		ctx := context.Background()
		st := factory(t)

		got, err := st.GetSandboxes(ctx)
		if err != nil {
			t.Fatalf("GetSandboxes() on empty store error = %v, want nil", err)
		}
		if got == nil {
			t.Fatal("GetSandboxes() on empty store = nil, want empty slice")
		}
		if len(got) != 0 {
			t.Errorf("GetSandboxes() on empty store = %#v, want no sandboxes", got)
		}
	})

	t.Run("ListReturnsEverySandbox", func(t *testing.T) {
		ctx := context.Background()
		st := factory(t)

		type expectedSandbox struct {
			image string
			state store.TaskState
		}

		want := map[string]expectedSandbox{}
		for _, image := range []string{"alpine:3.20", "debian:12", "ubuntu:24.04"} {
			created := createSandbox(t, ctx, st, "list-"+image, specFor(image))
			want[created.SandboxID] = expectedSandbox{image: image, state: store.Pending}
		}
		transitioned := createSandbox(t, ctx, st, "list-running", specFor("alpine:3.20"))
		transition(t, ctx, st, transitioned.SandboxID, store.Pending, store.Running)
		want[transitioned.SandboxID] = expectedSandbox{image: "alpine:3.20", state: store.Running}

		got, err := st.GetSandboxes(ctx)
		if err != nil {
			t.Fatalf("GetSandboxes() error = %v, want nil", err)
		}
		if len(got) != len(want) {
			t.Fatalf("GetSandboxes() returned %d sandboxes, want %d", len(got), len(want))
		}

		for _, sandbox := range got {
			expected, ok := want[sandbox.SandboxID]
			if !ok {
				t.Errorf("GetSandboxes() returned unexpected sandbox %q", sandbox.SandboxID)
				continue
			}
			delete(want, sandbox.SandboxID)

			if sandbox.Spec.Image == nil || *sandbox.Spec.Image != expected.image {
				t.Errorf("sandbox %q Spec.Image = %v, want %s", sandbox.SandboxID, sandbox.Spec.Image, expected.image)
			}
			if sandbox.State != expected.state {
				t.Errorf("sandbox %q State = %q, want %q", sandbox.SandboxID, sandbox.State, expected.state)
			}
			if sandbox.CreatedAt.IsZero() || sandbox.UpdatedAt.IsZero() {
				t.Errorf(
					"sandbox %q timestamps = (%v, %v), want both populated",
					sandbox.SandboxID, sandbox.CreatedAt, sandbox.UpdatedAt,
				)
			}
		}
		if len(want) != 0 {
			t.Errorf("GetSandboxes() omitted sandboxes: %v", want)
		}
	})

	t.Run("IdempotencyKeyReturnsSameSandbox", func(t *testing.T) {
		ctx := context.Background()
		st := factory(t)

		first := createSandbox(t, ctx, st, "same-operation", specFor("alpine:3.20"))
		second := createSandbox(t, ctx, st, "same-operation", specFor("debian:12"))

		if second.SandboxID != first.SandboxID {
			t.Errorf("retry sandbox ID = %q, want original ID %q", second.SandboxID, first.SandboxID)
		}
		if second.Spec.Image == nil || *second.Spec.Image != "alpine:3.20" {
			t.Errorf("retry Spec.Image = %v, want original request's alpine:3.20", second.Spec.Image)
		}

		key, err := st.GetIdempotencyKey(ctx, "same-operation")
		if err != nil {
			t.Fatalf("GetIdempotencyKey() error = %v, want nil", err)
		}
		if key == nil || key.SandboxID != first.SandboxID {
			t.Errorf("GetIdempotencyKey() = %#v, want sandbox ID %q", key, first.SandboxID)
		}
	})

	t.Run("StoreReturnsCopies", func(t *testing.T) {
		ctx := context.Background()
		st := factory(t)

		image := "alpine:3.20"
		first := createSandbox(t, ctx, st, "copy-semantics", store.SpecFile{
			Image: &image,
			Env:   map[string]string{"MODE": "test"},
		})
		first.State = store.Failed
		first.Spec.Env["MODE"] = "mutated"

		got, err := st.GetSandbox(ctx, first.SandboxID)
		if err != nil {
			t.Fatalf("GetSandbox(%s) error = %v, want nil", first.SandboxID, err)
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
		sandbox := createSandbox(t, ctx, st, "illegal-transition", specFor("alpine:3.20"))
		id := sandbox.SandboxID

		transition(t, ctx, st, id, store.Pending, store.Running)
		transition(t, ctx, st, id, store.Running, store.Stopping)
		transition(t, ctx, st, id, store.Stopping, store.Stopped)

		if _, err := st.UpdateSandboxState(ctx, id, store.Stopped, store.Running); !errors.Is(err, store.ErrInvalidStateTransition) {
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
		sandbox := createSandbox(t, ctx, st, "stale-transition", specFor("alpine:3.20"))
		id := sandbox.SandboxID

		if _, err := st.UpdateSandboxState(ctx, id, store.Running, store.Stopping); !errors.Is(err, store.ErrInvalidStateTransition) {
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

func specFor(image string) store.SpecFile {
	return store.SpecFile{Image: &image}
}

func createSandbox(
	t *testing.T,
	ctx context.Context,
	st store.Store,
	key string,
	spec store.SpecFile,
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

	sandbox, err := st.UpdateSandboxState(ctx, id, from, to)
	if err != nil {
		t.Fatalf("UpdateSandboxState(%q, %q, %q) error = %v, want nil", from, to, id, err)
	}
	if sandbox.State != to {
		t.Fatalf("UpdateSandboxState(%q, %q, %q) State = %q, want %q", from, to, id, sandbox.State, to)
	}
	return sandbox
}
