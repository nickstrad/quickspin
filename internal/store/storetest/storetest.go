package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nickstrad/quickspin/internal/sandbox"
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
		if created.State != sandbox.Pending {
			t.Errorf("CreateSandbox State = %q, want %q", created.State, sandbox.Pending)
		}
		if created.VersionID != 1 {
			t.Errorf("CreateSandbox VersionID = %d, want 1", created.VersionID)
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
		if got.SandboxID != created.SandboxID || got.VersionID != created.VersionID || got.State != created.State {
			t.Errorf("GetSandbox(%s) = %#v, want persisted identity and state from %#v", created.SandboxID, got, created)
		}
		if got.Spec.Image == nil || *got.Spec.Image != "alpine:3.20" {
			t.Errorf("GetSandbox(%s) Spec.Image = %v, want alpine:3.20", created.SandboxID, got.Spec.Image)
		}
	})

	// The expiry is what a reaper reads, so it has to survive both the round
	// trip and the state transitions the sandbox goes through on the way there.
	t.Run("ExpiryRoundTripsAndSurvivesTransitions", func(t *testing.T) {
		ctx := context.Background()
		st := factory(t)

		created := createSandbox(t, ctx, st, "expiry", specFor("alpine:3.20"))
		if !created.ExpiresAt.Equal(TestExpiry()) {
			t.Errorf("CreateSandbox ExpiresAt = %v, want %v", created.ExpiresAt, TestExpiry())
		}

		got, err := st.GetSandbox(ctx, created.SandboxID)
		if err != nil {
			t.Fatalf("GetSandbox(%s) error = %v, want nil", created.SandboxID, err)
		}
		if !got.ExpiresAt.Equal(TestExpiry()) {
			t.Errorf("GetSandbox ExpiresAt = %v, want %v", got.ExpiresAt, TestExpiry())
		}

		running := transition(t, ctx, st, created, sandbox.Running)
		if !running.ExpiresAt.Equal(TestExpiry()) {
			t.Errorf("UpdateSandboxState ExpiresAt = %v, want %v", running.ExpiresAt, TestExpiry())
		}
	})

	t.Run("ExpiryRenewalIsGuardedBySandboxState", func(t *testing.T) {
		tests := []struct {
			name        string
			state       sandbox.TaskState
			transitions []sandbox.TaskState
			renewable   bool
		}{
			{name: "pending sandbox is renewable", state: sandbox.Pending, renewable: true},
			{name: "running sandbox is renewable", state: sandbox.Running, transitions: []sandbox.TaskState{sandbox.Running}, renewable: true},
			{name: "failed sandbox is not renewable", state: sandbox.Failed, transitions: []sandbox.TaskState{sandbox.Failed}},
			{name: "stopping sandbox is not renewable", state: sandbox.Stopping, transitions: []sandbox.TaskState{sandbox.Running, sandbox.Stopping}},
			{name: "stopped sandbox is not renewable", state: sandbox.Stopped, transitions: []sandbox.TaskState{sandbox.Running, sandbox.Stopping, sandbox.Stopped}},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				ctx := context.Background()
				st := factory(t)
				sbx := createSandbox(t, ctx, st, "renew-"+string(tt.state), specFor("alpine:3.20"))
				for _, to := range tt.transitions {
					sbx = transition(t, ctx, st, sbx, to)
				}
				expiresAt := TestExpiry().Add(time.Hour)

				renewed, err := st.UpdateSandboxExpiry(ctx, sbx.SandboxID, expiresAt)
				if tt.renewable {
					if err != nil {
						t.Fatalf("UpdateSandboxExpiry(%s) error = %v, want nil", tt.state, err)
					}
					if renewed == nil {
						t.Fatalf("UpdateSandboxExpiry(%s) = nil, want a sandbox", tt.state)
					}
					if renewed.State != tt.state || !renewed.ExpiresAt.Equal(expiresAt) {
						t.Errorf("UpdateSandboxExpiry(%s) = %#v, want state %q and expiry %v", tt.state, renewed, tt.state, expiresAt)
					}
					if renewed.VersionID != sbx.VersionID {
						t.Errorf("UpdateSandboxExpiry(%s) VersionID = %d, want unchanged %d", tt.state, renewed.VersionID, sbx.VersionID)
					}
				} else if !errors.Is(err, sandbox.ErrInvalidStateTransition) {
					t.Fatalf("UpdateSandboxExpiry(%s) error = %v, want ErrInvalidStateTransition", tt.state, err)
				}

				persisted, err := st.GetSandbox(ctx, sbx.SandboxID)
				if err != nil {
					t.Fatalf("GetSandbox(%s) error = %v, want nil", sbx.SandboxID, err)
				}
				wantExpiry := TestExpiry()
				if tt.renewable {
					wantExpiry = expiresAt
				}
				if persisted.State != tt.state || !persisted.ExpiresAt.Equal(wantExpiry) {
					t.Errorf("persisted sandbox = %#v, want state %q and expiry %v", persisted, tt.state, wantExpiry)
				}
				if persisted.VersionID != sbx.VersionID {
					t.Errorf("persisted VersionID = %d, want unchanged %d", persisted.VersionID, sbx.VersionID)
				}
			})
		}
	})

	t.Run("ExpiryRenewalRequiresAnExistingSandbox", func(t *testing.T) {
		ctx := context.Background()
		st := factory(t)

		_, err := st.UpdateSandboxExpiry(ctx, "sbx_00000000-0000-0000-0000-000000000000", TestExpiry())
		if !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("UpdateSandboxExpiry(missing) error = %v, want ErrNotFound", err)
		}
	})

	t.Run("MissingRenewalExpiryRejected", func(t *testing.T) {
		ctx := context.Background()
		st := factory(t)
		sbx := createSandbox(t, ctx, st, "missing-renewal-expiry", specFor("alpine:3.20"))

		_, err := st.UpdateSandboxExpiry(ctx, sbx.SandboxID, time.Time{})
		if !errors.Is(err, store.ErrMissingExpiry) {
			t.Fatalf("UpdateSandboxExpiry(zero expiry) error = %v, want ErrMissingExpiry", err)
		}

		persisted, err := st.GetSandbox(ctx, sbx.SandboxID)
		if err != nil {
			t.Fatalf("GetSandbox(%s) error = %v, want nil", sbx.SandboxID, err)
		}
		if persisted.State != sandbox.Pending || !persisted.ExpiresAt.Equal(TestExpiry()) {
			t.Errorf("sandbox after rejected renewal = %#v, want pending with expiry %v", persisted, TestExpiry())
		}
	})

	t.Run("MissingExpiryRejected", func(t *testing.T) {
		ctx := context.Background()
		st := factory(t)

		_, err := st.CreateSandbox(ctx, "no-expiry", specFor("alpine:3.20"), time.Time{})
		if !errors.Is(err, store.ErrMissingExpiry) {
			t.Fatalf("CreateSandbox(zero expiry) error = %v, want ErrMissingExpiry", err)
		}

		sandboxes, err := st.GetSandboxes(ctx)
		if err != nil {
			t.Fatalf("GetSandboxes() error = %v, want nil", err)
		}
		if len(sandboxes) != 0 {
			t.Errorf("GetSandboxes() = %#v, want no row written for a rejected create", sandboxes)
		}
	})

	// Stored specs preserve omitted fields rather than materializing defaults.
	t.Run("EmptySpecIsStoredUnresolved", func(t *testing.T) {
		ctx := context.Background()
		st := factory(t)

		created := createSandbox(t, ctx, st, "empty-spec", sandbox.SpecFile{})

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
			state sandbox.TaskState
		}

		want := map[string]expectedSandbox{}
		for _, image := range []string{"alpine:3.20", "debian:12", "ubuntu:24.04"} {
			created := createSandbox(t, ctx, st, "list-"+image, specFor(image))
			want[created.SandboxID] = expectedSandbox{image: image, state: sandbox.Pending}
		}
		transitioned := createSandbox(t, ctx, st, "list-running", specFor("alpine:3.20"))
		transitioned = transition(t, ctx, st, transitioned, sandbox.Running)
		want[transitioned.SandboxID] = expectedSandbox{image: "alpine:3.20", state: sandbox.Running}

		got, err := st.GetSandboxes(ctx)
		if err != nil {
			t.Fatalf("GetSandboxes() error = %v, want nil", err)
		}
		if len(got) != len(want) {
			t.Fatalf("GetSandboxes() returned %d sandboxes, want %d", len(got), len(want))
		}

		for _, sbx := range got {
			expected, ok := want[sbx.SandboxID]
			if !ok {
				t.Errorf("GetSandboxes() returned unexpected sandbox %q", sbx.SandboxID)
				continue
			}
			delete(want, sbx.SandboxID)

			if sbx.Spec.Image == nil || *sbx.Spec.Image != expected.image {
				t.Errorf("sandbox %q Spec.Image = %v, want %s", sbx.SandboxID, sbx.Spec.Image, expected.image)
			}
			if sbx.State != expected.state {
				t.Errorf("sandbox %q State = %q, want %q", sbx.SandboxID, sbx.State, expected.state)
			}
			if sbx.CreatedAt.IsZero() || sbx.UpdatedAt.IsZero() {
				t.Errorf(
					"sandbox %q timestamps = (%v, %v), want both populated",
					sbx.SandboxID, sbx.CreatedAt, sbx.UpdatedAt,
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
		first := createSandbox(t, ctx, st, "copy-semantics", sandbox.SpecFile{
			Image: &image,
			Env:   map[string]string{"MODE": "test"},
		})
		first.State = sandbox.Failed
		first.Spec.Env["MODE"] = "mutated"

		got, err := st.GetSandbox(ctx, first.SandboxID)
		if err != nil {
			t.Fatalf("GetSandbox(%s) error = %v, want nil", first.SandboxID, err)
		}
		if got.State != sandbox.Pending {
			t.Errorf("re-read State = %q, want %q", got.State, sandbox.Pending)
		}
		if got.Spec.Env["MODE"] != "test" {
			t.Errorf("re-read Env[MODE] = %q, want test", got.Spec.Env["MODE"])
		}
	})

	t.Run("IllegalTransitionRejected", func(t *testing.T) {
		ctx := context.Background()
		st := factory(t)
		sbx := createSandbox(t, ctx, st, "illegal-transition", specFor("alpine:3.20"))
		id := sbx.SandboxID

		current := transition(t, ctx, st, sbx, sandbox.Running)
		current = transition(t, ctx, st, current, sandbox.Stopping)
		stopped := transition(t, ctx, st, current, sandbox.Stopped)

		if _, err := st.UpdateSandboxState(ctx, id, sandbox.Stopped, sandbox.Running, "illegal", stopped.VersionID); !errors.Is(err, sandbox.ErrInvalidStateTransition) {
			t.Fatalf("UpdateSandboxState(stopped, running) error = %v, want ErrInvalidStateTransition", err)
		}

		got, err := st.GetSandbox(ctx, id)
		if err != nil {
			t.Fatalf("GetSandbox(%s) error = %v, want nil", id, err)
		}
		if got.State != sandbox.Stopped {
			t.Errorf("State after rejected transition = %q, want %q", got.State, sandbox.Stopped)
		}
	})

	// The invariant the append-only log exists for: history is never recomputed,
	// so replaying it has to land on the state the row reports.
	t.Run("EveryTransitionEmitsEvent", func(t *testing.T) {
		ctx := context.Background()
		st := factory(t)

		sbx := createSandbox(t, ctx, st, "events", specFor("alpine:3.20"))
		id := sbx.SandboxID
		sbx = transition(t, ctx, st, sbx, sandbox.Running)
		sbx = transition(t, ctx, st, sbx, sandbox.Stopping)
		final := transition(t, ctx, st, sbx, sandbox.Stopped)

		got, err := st.GetSandboxEvents(ctx, id)
		if err != nil {
			t.Fatalf("GetSandboxEvents(%s) error = %v, want nil", id, err)
		}
		if len(got) != 4 {
			t.Fatalf("GetSandboxEvents(%s) returned %d events, want the create plus 3 transitions", id, len(got))
		}

		var replayed sandbox.TaskState
		for i, e := range got {
			if e.SandboxID != id {
				t.Errorf("event %d sandbox id = %q, want %q", i, e.SandboxID, id)
			}
			if e.Reason == "" {
				t.Errorf("event %d has no reason, want the caller's", i)
			}
			if e.At.IsZero() {
				t.Errorf("event %d At is zero, want the instant of the transition", i)
			}
			if e.VersionID != i+1 {
				t.Errorf("event %d VersionID = %d, want %d", i, e.VersionID, i+1)
			}
			if e.FromState != replayed {
				t.Fatalf("event %d from state = %q, want the previous event's to state %q", i, e.FromState, replayed)
			}
			replayed = e.ToState
		}
		if replayed != final.State {
			t.Errorf("replayed state = %q, want the sandbox's current %q", replayed, final.State)
		}
		if final.VersionID != len(got) {
			t.Errorf("sandbox VersionID = %d, want final event version %d", final.VersionID, len(got))
		}
	})

	t.Run("RejectedTransitionEmitsNoEvent", func(t *testing.T) {
		ctx := context.Background()
		st := factory(t)

		created := createSandbox(t, ctx, st, "no-event", specFor("alpine:3.20"))

		if _, err := st.UpdateSandboxState(ctx, created.SandboxID, sandbox.Running, sandbox.Stopping, "never happened", created.VersionID); err == nil {
			t.Fatal("UpdateSandboxState(running, stopping) on a pending row error = nil, want a rejection")
		}

		got, err := st.GetSandboxEvents(ctx, created.SandboxID)
		if err != nil {
			t.Fatalf("GetSandboxEvents(%s) error = %v, want nil", created.SandboxID, err)
		}
		if len(got) != 1 {
			t.Errorf("GetSandboxEvents(%s) returned %d events, want only the create", created.SandboxID, len(got))
		}
	})

	t.Run("EventsAreScopedToTheirSandbox", func(t *testing.T) {
		ctx := context.Background()
		st := factory(t)

		first := createSandbox(t, ctx, st, "first", specFor("alpine:3.20"))
		second := createSandbox(t, ctx, st, "second", specFor("debian:12")).SandboxID
		transition(t, ctx, st, first, sandbox.Running)

		got, err := st.GetSandboxEvents(ctx, second)
		if err != nil {
			t.Fatalf("GetSandboxEvents(%s) error = %v, want nil", second, err)
		}
		if len(got) != 1 || got[0].SandboxID != second {
			t.Errorf("GetSandboxEvents(%s) = %+v, want only its own create event", second, got)
		}
	})

	t.Run("EventListsAreEmptySlicesNotNil", func(t *testing.T) {
		ctx := context.Background()
		st := factory(t)

		unknown, err := st.GetSandboxEvents(ctx, "sbx_00000000-0000-0000-0000-000000000000")
		if err != nil {
			t.Fatalf("GetSandboxEvents(missing) error = %v, want nil", err)
		}
		if unknown == nil || len(unknown) != 0 {
			t.Errorf("GetSandboxEvents(missing) = %#v, want an empty non-nil slice", unknown)
		}
	})

	t.Run("TransitionRequiresCurrentFromState", func(t *testing.T) {
		ctx := context.Background()
		st := factory(t)
		sbx := createSandbox(t, ctx, st, "stale-transition", specFor("alpine:3.20"))
		id := sbx.SandboxID

		if _, err := st.UpdateSandboxState(ctx, id, sandbox.Running, sandbox.Stopping, "stale", sbx.VersionID); !errors.Is(err, sandbox.ErrInvalidStateTransition) {
			t.Fatalf(
				"UpdateSandboxState(running, stopping) for a pending row error = %v, want ErrInvalidStateTransition",
				err,
			)
		}

		got, err := st.GetSandbox(ctx, id)
		if err != nil {
			t.Fatalf("GetSandbox(%s) error = %v, want nil", id, err)
		}
		if got.State != sandbox.Pending {
			t.Errorf("State after stale transition = %q, want %q", got.State, sandbox.Pending)
		}
	})

	t.Run("TransitionRequiresCurrentVersion", func(t *testing.T) {
		ctx := context.Background()
		st := factory(t)
		created := createSandbox(t, ctx, st, "stale-version", specFor("alpine:3.20"))
		running := transition(t, ctx, st, created, sandbox.Running)

		_, err := st.UpdateSandboxState(ctx, created.SandboxID, sandbox.Running, sandbox.Stopping, "stale generation", created.VersionID)
		if !errors.Is(err, sandbox.ErrInvalidStateTransition) {
			t.Fatalf("UpdateSandboxState with version %d error = %v, want ErrInvalidStateTransition", created.VersionID, err)
		}

		persisted, err := st.GetSandbox(ctx, created.SandboxID)
		if err != nil {
			t.Fatalf("GetSandbox(%s) error = %v, want nil", created.SandboxID, err)
		}
		if persisted.State != sandbox.Running || persisted.VersionID != running.VersionID {
			t.Errorf("sandbox after stale version = %#v, want running at version %d", persisted, running.VersionID)
		}

		events, err := st.GetSandboxEvents(ctx, created.SandboxID)
		if err != nil {
			t.Fatalf("GetSandboxEvents(%s) error = %v, want nil", created.SandboxID, err)
		}
		if len(events) != 2 {
			t.Errorf("events after stale version = %d, want create plus successful transition", len(events))
		}
	})
}

func specFor(image string) sandbox.SpecFile {
	return sandbox.SpecFile{Image: &image}
}

// Fixed and rounded so a store that loses sub-second precision on the round
// trip still compares equal.
func TestExpiry() time.Time {
	return time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
}

func createSandbox(
	t *testing.T,
	ctx context.Context,
	st store.Store,
	key string,
	spec sandbox.SpecFile,
) *sandbox.Sandbox {
	t.Helper()

	sbx, err := st.CreateSandbox(ctx, key, spec, TestExpiry())
	if err != nil {
		t.Fatalf("CreateSandbox(%q) error = %v, want nil", key, err)
	}
	return sbx
}

func transition(
	t *testing.T,
	ctx context.Context,
	st store.Store,
	before *sandbox.Sandbox,
	to sandbox.TaskState,
) *sandbox.Sandbox {
	t.Helper()

	sbx, err := st.UpdateSandboxState(ctx, before.SandboxID, before.State, to, "storetest transition", before.VersionID)
	if err != nil {
		t.Fatalf("UpdateSandboxState(%q, %q, %q) error = %v, want nil", before.SandboxID, before.State, to, err)
	}
	if sbx == nil {
		t.Fatalf("UpdateSandboxState(%q, %q, %q) = nil, want a sandbox", before.SandboxID, before.State, to)
	}
	if sbx.State != to {
		t.Fatalf("UpdateSandboxState(%q, %q, %q) State = %q, want %q", before.SandboxID, before.State, to, sbx.State, to)
	}
	if sbx.VersionID != before.VersionID+1 {
		t.Fatalf("UpdateSandboxState(%q, %q, %q) VersionID = %d, want %d", before.SandboxID, before.State, to, sbx.VersionID, before.VersionID+1)
	}
	return sbx
}
