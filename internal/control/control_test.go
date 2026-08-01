package control

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/nickstrad/quickspin/internal/runtime"
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
	createSandbox      func(ctx context.Context, key string, spec sandbox.SpecFile) (*sandbox.Sandbox, error)
	getSandbox         func(ctx context.Context, sandboxID string) (*sandbox.Sandbox, error)
	updateSandboxState func(ctx context.Context, sandboxID string, from, to sandbox.TaskState) (*sandbox.Sandbox, error)
}

func (f *fakeStore) CreateSandbox(ctx context.Context, key string, spec sandbox.SpecFile) (*sandbox.Sandbox, error) {
	return f.createSandbox(ctx, key, spec)
}

func (f *fakeStore) GetSandbox(ctx context.Context, sandboxID string) (*sandbox.Sandbox, error) {
	return f.getSandbox(ctx, sandboxID)
}

func (f *fakeStore) UpdateSandboxState(ctx context.Context, sandboxID string, from, to sandbox.TaskState) (*sandbox.Sandbox, error) {
	return f.updateSandboxState(ctx, sandboxID, from, to)
}

func ptr[T any](v T) *T { return &v }

// The row is committed before the runtime is asked for anything, so a create
// that fails has to be recorded on it: a sandbox left in pending is
// indistinguishable from one still starting.
func TestCreateSandboxMarksTheSandboxFailedWhenTheRuntimeRefuses(t *testing.T) {
	st := newTestStore(t)
	boom := errors.New("no such image")
	c := New(discardLogger(), st, runtimetest.Fake{
		CreateFn: func(context.Context, string, runtime.Spec) (runtime.Info, error) {
			return runtime.Info{}, boom
		},
	})

	sbx, err := c.CreateSandbox(context.Background(), "k1", sandbox.SpecFile{})
	if !errors.Is(err, boom) {
		t.Fatalf("CreateSandbox() error = %v, want the runtime failure", err)
	}
	if sbx != nil {
		t.Errorf("CreateSandbox() sandbox = %#v, want nil", sbx)
	}

	sandboxes, err := st.GetSandboxes(context.Background())
	if err != nil {
		t.Fatalf("GetSandboxes() error = %v, want nil", err)
	}
	if len(sandboxes) != 1 {
		t.Fatalf("GetSandboxes() returned %d sandboxes, want 1", len(sandboxes))
	}
	if sandboxes[0].State != sandbox.Failed {
		t.Errorf("State = %q, want %q", sandboxes[0].State, sandbox.Failed)
	}
}

// The compensation is best-effort: the caller is already on its way to a 5xx,
// so a store that cannot record the failure must not replace the runtime error
// the caller is about to classify.
func TestCreateSandboxKeepsTheRuntimeErrorWhenCompensationFails(t *testing.T) {
	boom := errors.New("no such image")
	var compensated bool
	st := &fakeStore{
		createSandbox: func(context.Context, string, sandbox.SpecFile) (*sandbox.Sandbox, error) {
			return &sandbox.Sandbox{SandboxID: "sbx_1", State: sandbox.Pending}, nil
		},
		updateSandboxState: func(_ context.Context, _ string, from, to sandbox.TaskState) (*sandbox.Sandbox, error) {
			compensated = from == sandbox.Pending && to == sandbox.Failed
			return nil, errors.New("database is on fire")
		},
	}
	c := New(discardLogger(), st, runtimetest.Fake{
		CreateFn: func(context.Context, string, runtime.Spec) (runtime.Info, error) {
			return runtime.Info{}, boom
		},
	})

	_, err := c.CreateSandbox(context.Background(), "k1", sandbox.SpecFile{})
	if !errors.Is(err, boom) {
		t.Fatalf("CreateSandbox() error = %v, want the runtime failure", err)
	}
	if !compensated {
		t.Error("the compensating pending→failed transition was never attempted")
	}
}

// A repeated key must not start a second container: the record already has one.
func TestCreateSandboxReplaysWithoutStartingASecondRuntime(t *testing.T) {
	st := newTestStore(t)
	var creates int
	c := New(discardLogger(), st, runtimetest.Fake{
		CreateFn: func(context.Context, string, runtime.Spec) (runtime.Info, error) {
			creates++
			return runtime.Info{}, nil
		},
	})

	first, err := c.CreateSandbox(context.Background(), "same-operation", sandbox.SpecFile{})
	if err != nil {
		t.Fatalf("CreateSandbox() error = %v, want nil", err)
	}
	second, err := c.CreateSandbox(context.Background(), "same-operation", sandbox.SpecFile{Image: ptr("debian:12")})
	if err != nil {
		t.Fatalf("replayed CreateSandbox() error = %v, want nil", err)
	}

	if creates != 1 {
		t.Errorf("runtime.Create called %d times, want 1", creates)
	}
	if second.SandboxID != first.SandboxID {
		t.Errorf("replay sandbox id = %q, want the original %q", second.SandboxID, first.SandboxID)
	}
	if second.State != sandbox.Running {
		t.Errorf("replay state = %q, want %q", second.State, sandbox.Running)
	}
}

// A spec that cannot be resolved must not leave a row behind: the fake store
// panics on any call, so reaching the write fails the test.
func TestCreateSandboxRejectsAnUnresolvableSpecBeforeTheStoreWrite(t *testing.T) {
	c := New(discardLogger(), &fakeStore{}, runtimetest.Fake{})

	_, err := c.CreateSandbox(context.Background(), "k1", sandbox.SpecFile{Memory: ptr("12x")})
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
		createSandbox: func(context.Context, string, sandbox.SpecFile) (*sandbox.Sandbox, error) {
			return nil, store.ErrNotFound
		},
	}, runtimetest.Fake{})

	_, err := c.CreateSandbox(context.Background(), "k1", sandbox.SpecFile{})
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
		createSandbox: func(context.Context, string, sandbox.SpecFile) (*sandbox.Sandbox, error) {
			return nil, sandbox.ErrInvalidSpec
		},
	}, runtimetest.Fake{})

	_, err := c.CreateSandbox(context.Background(), "k1", sandbox.SpecFile{})
	if errors.Is(err, ErrInternal) {
		t.Errorf("CreateSandbox() error = %v, want no ErrInternal marker", err)
	}
}

// The container is running by the time this transition fails, so a row left in
// pending is ours to answer for.
func TestCreateSandboxMarksAFailedRunningTransitionInternal(t *testing.T) {
	c := New(discardLogger(), &fakeStore{
		createSandbox: func(context.Context, string, sandbox.SpecFile) (*sandbox.Sandbox, error) {
			return &sandbox.Sandbox{SandboxID: "sbx_1", State: sandbox.Pending}, nil
		},
		updateSandboxState: func(context.Context, string, sandbox.TaskState, sandbox.TaskState) (*sandbox.Sandbox, error) {
			return nil, sandbox.ErrInvalidStateTransition
		},
	}, runtimetest.Fake{
		CreateFn: func(context.Context, string, runtime.Spec) (runtime.Info, error) {
			return runtime.Info{}, nil
		},
	})

	_, err := c.CreateSandbox(context.Background(), "k1", sandbox.SpecFile{})
	if !errors.Is(err, ErrInternal) {
		t.Errorf("CreateSandbox() error = %v, want the ErrInternal marker", err)
	}
}

func TestDestroySandboxWalksRunningToStopped(t *testing.T) {
	st := newTestStore(t)
	c := New(discardLogger(), st, runtimetest.Fake{
		CreateFn: func(context.Context, string, runtime.Spec) (runtime.Info, error) {
			return runtime.Info{}, nil
		},
		DestroyFn: func(context.Context, string) error { return nil },
	})

	sbx, err := c.CreateSandbox(context.Background(), "k1", sandbox.SpecFile{})
	if err != nil {
		t.Fatalf("CreateSandbox() error = %v, want nil", err)
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
				updateSandboxState: func(context.Context, string, sandbox.TaskState, sandbox.TaskState) (*sandbox.Sandbox, error) {
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
