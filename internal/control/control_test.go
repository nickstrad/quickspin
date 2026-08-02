package control

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/nickstrad/quickspin/internal/runtime/runtimetest"
	"github.com/nickstrad/quickspin/internal/sandbox"
	"github.com/nickstrad/quickspin/internal/store"
	"github.com/nickstrad/quickspin/internal/store/sqlite"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newTestStore(t *testing.T) *sqlite.Store {
	t.Helper()

	st, err := sqlite.New(context.Background(), ":memory:", "", discardLogger())
	if err != nil {
		t.Fatalf("sqlite.New(:memory:) error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := st.Cleanup(); err != nil {
			t.Errorf("Cleanup() error = %v, want nil", err)
		}
	})
	return st
}

// fakeStore scripts one method at a time. Every unset method is nil, so a call
// the case did not intend panics the test rather than silently succeeding.
type fakeStore struct {
	store.Store
	createSandbox      func(ctx context.Context, key string, spec sandbox.SpecFile, expiresAt time.Time) (*sandbox.Sandbox, error)
	getSandbox         func(ctx context.Context, sandboxID string) (*sandbox.Sandbox, error)
	updateSandboxState func(ctx context.Context, sandboxID string, from, to sandbox.TaskState, reason string) (*sandbox.Sandbox, error)
}

func (f *fakeStore) CreateSandbox(ctx context.Context, key string, spec sandbox.SpecFile, expiresAt time.Time) (*sandbox.Sandbox, error) {
	return f.createSandbox(ctx, key, spec, expiresAt)
}

func (f *fakeStore) GetSandbox(ctx context.Context, sandboxID string) (*sandbox.Sandbox, error) {
	return f.getSandbox(ctx, sandboxID)
}

func (f *fakeStore) UpdateSandboxState(ctx context.Context, sandboxID string, from, to sandbox.TaskState, reason string) (*sandbox.Sandbox, error) {
	return f.updateSandboxState(ctx, sandboxID, from, to, reason)
}

func ptr[T any](v T) *T { return &v }

// Create records intent and stops. The container is the reconciler's to make,
// which is what lets a crash between the two cost a tick rather than a sandbox.
// The Fake panics on an unset CreateFn, so a create that reaches the runtime
// fails here.
func TestCreateSandboxRecordsAPendingSandboxAndStartsNoRuntime(t *testing.T) {
	st := newTestStore(t)
	c := New(discardLogger(), st, runtimetest.Fake{})

	sbx, err := c.CreateSandbox(context.Background(), "k1", sandbox.SpecFile{}, 0)
	if err != nil {
		t.Fatalf("CreateSandbox() error = %v, want nil", err)
	}
	if sbx.State != sandbox.Pending {
		t.Errorf("State = %q, want %q", sbx.State, sandbox.Pending)
	}

	stored, err := st.GetSandbox(context.Background(), sbx.SandboxID)
	if err != nil {
		t.Fatalf("GetSandbox() error = %v, want nil", err)
	}
	if stored.State != sandbox.Pending {
		t.Errorf("stored State = %q, want %q", stored.State, sandbox.Pending)
	}
}

// A repeated key returns the original record rather than writing a second one.
func TestCreateSandboxReplaysTheOriginalRecord(t *testing.T) {
	st := newTestStore(t)
	c := New(discardLogger(), st, runtimetest.Fake{})

	first, err := c.CreateSandbox(context.Background(), "same-operation", sandbox.SpecFile{}, 0)
	if err != nil {
		t.Fatalf("CreateSandbox() error = %v, want nil", err)
	}
	second, err := c.CreateSandbox(context.Background(), "same-operation", sandbox.SpecFile{Image: ptr("debian:12")}, 0)
	if err != nil {
		t.Fatalf("replayed CreateSandbox() error = %v, want nil", err)
	}

	if second.SandboxID != first.SandboxID {
		t.Errorf("replay sandbox id = %q, want the original %q", second.SandboxID, first.SandboxID)
	}
	if second.Spec.Image != nil {
		t.Errorf("replay spec image = %v, want the original request's nil", second.Spec.Image)
	}

	sandboxes, err := st.GetSandboxes(context.Background())
	if err != nil {
		t.Fatalf("GetSandboxes() error = %v, want nil", err)
	}
	if len(sandboxes) != 1 {
		t.Errorf("GetSandboxes() returned %d sandboxes, want 1", len(sandboxes))
	}
}

func TestCreateSandboxTurnsTheTTLIntoAnAbsoluteExpiry(t *testing.T) {
	now := time.Date(2030, time.January, 2, 3, 4, 5, 0, time.UTC)
	tests := []struct {
		name string
		ttl  time.Duration
		want time.Time
	}{
		{name: "explicit ttl", ttl: 90 * time.Second, want: now.Add(90 * time.Second)},
		{name: "omitted ttl takes the default", ttl: 0, want: now.Add(sandbox.DefaultTTL)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got time.Time
			c := New(discardLogger(), &fakeStore{
				createSandbox: func(_ context.Context, _ string, _ sandbox.SpecFile, expiresAt time.Time) (*sandbox.Sandbox, error) {
					got = expiresAt
					return &sandbox.Sandbox{SandboxID: "sbx_1", State: sandbox.Pending, ExpiresAt: expiresAt}, nil
				},
			}, runtimetest.Fake{})
			c.now = func() time.Time { return now }

			if _, err := c.CreateSandbox(context.Background(), "k1", sandbox.SpecFile{}, tt.ttl); err != nil {
				t.Fatalf("CreateSandbox() error = %v, want nil", err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("stored ExpiresAt = %v, want %v", got, tt.want)
			}
		})
	}
}

// The fake store panics on any call, so reaching the write fails the test.
func TestCreateSandboxRejectsAnOverCapTTLBeforeTheStoreWrite(t *testing.T) {
	c := New(discardLogger(), &fakeStore{}, runtimetest.Fake{})

	_, err := c.CreateSandbox(context.Background(), "k1", sandbox.SpecFile{}, sandbox.MaxTTL+time.Second)
	if !errors.Is(err, sandbox.ErrInvalidSpec) {
		t.Fatalf("CreateSandbox() error = %v, want sandbox.ErrInvalidSpec", err)
	}
	if errors.Is(err, ErrInternal) {
		t.Error("an over-cap ttl is the caller's mistake, want no ErrInternal marker")
	}
}

// A spec that cannot be resolved must not leave a row behind: the fake store
// panics on any call, so reaching the write fails the test.
func TestCreateSandboxRejectsAnUnresolvableSpecBeforeTheStoreWrite(t *testing.T) {
	c := New(discardLogger(), &fakeStore{}, runtimetest.Fake{})

	_, err := c.CreateSandbox(context.Background(), "k1", sandbox.SpecFile{Memory: ptr("12x")}, 0)
	if !errors.Is(err, sandbox.ErrInvalidSpec) {
		t.Fatalf("CreateSandbox() error = %v, want sandbox.ErrInvalidSpec", err)
	}
	if errors.Is(err, ErrInternal) {
		t.Error("an unresolvable spec is the caller's mistake, want no ErrInternal marker")
	}
}

// An idempotency key pointing at a row that no longer exists is our
// inconsistency, so the store's not-found sentinel must not read as a 404.
func TestCreateSandboxMarksAStoreFailureInternal(t *testing.T) {
	c := New(discardLogger(), &fakeStore{
		createSandbox: func(context.Context, string, sandbox.SpecFile, time.Time) (*sandbox.Sandbox, error) {
			return nil, store.ErrNotFound
		},
	}, runtimetest.Fake{})

	_, err := c.CreateSandbox(context.Background(), "k1", sandbox.SpecFile{}, 0)
	if !errors.Is(err, ErrInternal) {
		t.Errorf("CreateSandbox() error = %v, want the ErrInternal marker", err)
	}
	if !errors.Is(err, store.ErrNotFound) {
		t.Errorf("CreateSandbox() error = %v, want the store cause preserved", err)
	}
}

// An invalid spec rejected by the store stays the caller's mistake.
func TestCreateSandboxLeavesAnInvalidSpecFromTheStoreAsTheCallersFault(t *testing.T) {
	c := New(discardLogger(), &fakeStore{
		createSandbox: func(context.Context, string, sandbox.SpecFile, time.Time) (*sandbox.Sandbox, error) {
			return nil, sandbox.ErrInvalidSpec
		},
	}, runtimetest.Fake{})

	_, err := c.CreateSandbox(context.Background(), "k1", sandbox.SpecFile{}, 0)
	if errors.Is(err, ErrInternal) {
		t.Errorf("CreateSandbox() error = %v, want no ErrInternal marker", err)
	}
}

func TestDestroySandboxWalksRunningToStopped(t *testing.T) {
	st := newTestStore(t)
	c := New(discardLogger(), st, runtimetest.Fake{
		DestroyFn: func(context.Context, string) error { return nil },
	})

	sbx, err := c.CreateSandbox(context.Background(), "k1", sandbox.SpecFile{}, 0)
	if err != nil {
		t.Fatalf("CreateSandbox() error = %v, want nil", err)
	}
	// Stands in for the reconciler, which is what moves a sandbox to running.
	if _, err := st.UpdateSandboxState(context.Background(), sbx.SandboxID, sandbox.Pending, sandbox.Running, "test"); err != nil {
		t.Fatalf("UpdateSandboxState(pending, running) error = %v, want nil", err)
	}

	if err := c.DestroySandbox(context.Background(), sbx.SandboxID); err != nil {
		t.Fatalf("DestroySandbox() error = %v, want nil", err)
	}

	stopped, err := st.GetSandbox(context.Background(), sbx.SandboxID)
	if err != nil {
		t.Fatalf("GetSandbox() error = %v, want nil", err)
	}
	if stopped.State != sandbox.Stopped {
		t.Errorf("State = %q, want %q", stopped.State, sandbox.Stopped)
	}
}

// DELETE is idempotent, so nothing to stop is a success — and the runtime is
// never asked, which the Fake enforces by panicking on an unset DestroyFn.
func TestDestroySandboxIsASuccessWhenNothingIsRunning(t *testing.T) {
	tests := []struct {
		name      string
		storeErr  error
		wantError bool
	}{
		{name: "no such row", storeErr: store.ErrNotFound},
		{name: "already past running", storeErr: sandbox.ErrInvalidStateTransition},
		{name: "store failure", storeErr: errors.New("database is on fire"), wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New(discardLogger(), &fakeStore{
				updateSandboxState: func(context.Context, string, sandbox.TaskState, sandbox.TaskState, string) (*sandbox.Sandbox, error) {
					return nil, tt.storeErr
				},
			}, runtimetest.Fake{})

			err := c.DestroySandbox(context.Background(), "sbx_1")
			if tt.wantError && !errors.Is(err, tt.storeErr) {
				t.Fatalf("DestroySandbox() error = %v, want %v", err, tt.storeErr)
			}
			if !tt.wantError && err != nil {
				t.Fatalf("DestroySandbox() error = %v, want nil", err)
			}
		})
	}
}

func TestRequireRunning(t *testing.T) {
	tests := []struct {
		name     string
		sbx      *sandbox.Sandbox
		storeErr error
		want     error
	}{
		{name: "running", sbx: &sandbox.Sandbox{State: sandbox.Running}},
		{name: "pending", sbx: &sandbox.Sandbox{State: sandbox.Pending}, want: sandbox.ErrSandboxNotRunning},
		{name: "stopped", sbx: &sandbox.Sandbox{State: sandbox.Stopped}, want: sandbox.ErrSandboxNotRunning},
		{name: "unknown id", storeErr: store.ErrNotFound, want: store.ErrNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New(discardLogger(), &fakeStore{
				getSandbox: func(context.Context, string) (*sandbox.Sandbox, error) {
					return tt.sbx, tt.storeErr
				},
			}, runtimetest.Fake{})

			err := c.RequireRunning(context.Background(), "sbx_1")
			if tt.want == nil {
				if err != nil {
					t.Fatalf("RequireRunning() error = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tt.want) {
				t.Fatalf("RequireRunning() error = %v, want %v", err, tt.want)
			}
		})
	}
}
