package reconciler

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/quickspin/internal/control"
	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/nickstrad/quickspin/internal/runtime/runtimetest"
	"github.com/nickstrad/quickspin/internal/sandbox"
	"github.com/nickstrad/quickspin/internal/store"
	"github.com/nickstrad/quickspin/internal/store/sqlite"
	"github.com/nickstrad/quickspin/internal/store/storetest"
)

// Cases come from the decision table and plan 06, not from the current
// implementation, so a disagreement here means a logic gap, not a stale test.
func TestDecideReconcileAction(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	live := now.Add(time.Hour)
	expired := now.Add(-time.Hour)

	sbx := func(state sandbox.TaskState, expiresAt time.Time) *sandbox.Sandbox {
		return &sandbox.Sandbox{SandboxID: "sbx-1", State: state, ExpiresAt: expiresAt}
	}
	info := func(state runtime.State) *runtime.Info {
		return &runtime.Info{ID: "sbx-1", State: state}
	}

	tests := []struct {
		name     string
		desired  *sandbox.Sandbox
		observed *runtime.Info
		want     ReconcileAction
	}{
		// row nil: the DB is authoritative, labeled containers without a row die
		{"orphan running container is destroyed", nil, info(runtime.StateRunning), ActionDestroyOrphan},
		{"orphan exited container is destroyed", nil, info(runtime.StateStopped), ActionDestroyOrphan},
		{"both absent is a safe no-op", nil, nil, ActionNone},

		// row pending
		{"pending row with no container is created", sbx(sandbox.Pending, live), nil, ActionCreate},
		{"pending row with running container adopts the container", sbx(sandbox.Pending, live), info(runtime.StateRunning), ActionMarkRunning},
		{"pending row whose container already exited is marked failed", sbx(sandbox.Pending, live), info(runtime.StateStopped), ActionMarkFailed},

		// row running
		{"running row with vanished container is marked failed", sbx(sandbox.Running, live), nil, ActionMarkFailed},
		{"running row with running container is converged", sbx(sandbox.Running, live), info(runtime.StateRunning), ActionNone},
		{"running row with exited container is marked failed", sbx(sandbox.Running, live), info(runtime.StateStopped), ActionMarkFailed},

		// row stopping
		{"stopping row with no container transitions to stopped", sbx(sandbox.Stopping, live), nil, ActionMarkStopped},
		{"stopping row with running container is destroyed", sbx(sandbox.Stopping, live), info(runtime.StateRunning), ActionDestroy},
		{"stopping row with exited container is destroyed", sbx(sandbox.Stopping, live), info(runtime.StateStopped), ActionDestroy},

		// terminal rows: converged when the container is gone, cleanup otherwise
		{"stopped row with no container is converged", sbx(sandbox.Stopped, live), nil, ActionNone},
		{"stopped row with running container is destroyed", sbx(sandbox.Stopped, live), info(runtime.StateRunning), ActionDestroy},
		{"stopped row with exited container is destroyed", sbx(sandbox.Stopped, live), info(runtime.StateStopped), ActionDestroy},
		{"failed row with no container is converged", sbx(sandbox.Failed, live), nil, ActionNone},
		{"failed row with running container is destroyed", sbx(sandbox.Failed, live), info(runtime.StateRunning), ActionDestroy},
		{"failed row with exited container is destroyed", sbx(sandbox.Failed, live), info(runtime.StateStopped), ActionDestroy},

		// expiry overrides every non-terminal cell, container or not
		{"expired pending row with no container is reaped", sbx(sandbox.Pending, expired), nil, ActionReap},
		{"expired pending row with running container is reaped", sbx(sandbox.Pending, expired), info(runtime.StateRunning), ActionReap},
		{"expired running row with running container is reaped", sbx(sandbox.Running, expired), info(runtime.StateRunning), ActionReap},
		{"expired running row with vanished container is reaped", sbx(sandbox.Running, expired), nil, ActionReap},
		{"expired running row with exited container is reaped", sbx(sandbox.Running, expired), info(runtime.StateStopped), ActionReap},
		{"expired stopping row is reaped", sbx(sandbox.Stopping, expired), info(runtime.StateRunning), ActionReap},

		// terminal rows are already past their lifecycle: expiry must not change them
		{"expired stopped row with leftover container is still destroyed", sbx(sandbox.Stopped, expired), info(runtime.StateRunning), ActionDestroy},
		{"expired failed row with no container stays converged", sbx(sandbox.Failed, expired), nil, ActionNone},

		// expiry is strict: at the exact deadline the row is not yet expired
		{"row at exactly ExpiresAt is not reaped", sbx(sandbox.Running, now), info(runtime.StateRunning), ActionNone},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decideReconcileAction(tt.desired, tt.observed, now); got != tt.want {
				t.Errorf("decideReconcileAction(%v, %v, now) = %q, want %q", tt.desired, tt.observed, got, tt.want)
			}
		})
	}
}

// The join is what decides which cell of the decision table each sandbox lands
// in, so the cases assert pairing and order, not the action that follows.
func TestPairSnapshots(t *testing.T) {
	row := func(id string) *sandbox.Sandbox { return &sandbox.Sandbox{SandboxID: id} }
	container := func(id string) runtime.Info { return runtime.Info{ID: id, State: runtime.StateRunning} }

	// want records, per expected item in order, its id and which sides paired.
	type wantItem struct {
		id          string
		hasDesired  bool
		hasObserved bool
	}

	tests := []struct {
		name  string
		sbxs  []*sandbox.Sandbox
		infos []runtime.Info
		want  []wantItem
	}{
		{
			name: "both snapshots empty pairs nothing",
		},
		{
			name:  "row pairs with its container",
			sbxs:  []*sandbox.Sandbox{row("sbx-1")},
			infos: []runtime.Info{container("sbx-1")},
			want:  []wantItem{{"sbx-1", true, true}},
		},
		{
			name: "row without a container pairs with nil",
			sbxs: []*sandbox.Sandbox{row("sbx-1")},
			want: []wantItem{{"sbx-1", true, false}},
		},
		{
			name:  "container without a row becomes an orphan",
			infos: []runtime.Info{container("sbx-orphan")},
			want:  []wantItem{{"sbx-orphan", false, true}},
		},
		{
			name:  "mixed snapshots are joined once each in id order",
			sbxs:  []*sandbox.Sandbox{row("sbx-c"), row("sbx-a"), row("sbx-d")},
			infos: []runtime.Info{container("sbx-b"), container("sbx-d"), container("sbx-a")},
			want: []wantItem{
				{"sbx-a", true, true},
				{"sbx-b", false, true},
				{"sbx-c", true, false},
				{"sbx-d", true, true},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pairSnapshots(tt.sbxs, tt.infos)

			if len(got) != len(tt.want) {
				t.Fatalf("pairSnapshots() returned %d items, want %d", len(got), len(tt.want))
			}
			for i, want := range tt.want {
				item := got[i]
				if item.id() != want.id {
					t.Errorf("item %d id = %q, want %q", i, item.id(), want.id)
				}
				if (item.desired != nil) != want.hasDesired {
					t.Errorf("item %d desired = %v, want present %t", i, item.desired, want.hasDesired)
				}
				if (item.observed != nil) != want.hasObserved {
					t.Errorf("item %d observed = %v, want present %t", i, item.observed, want.hasObserved)
				}
				if item.observed != nil && item.observed.ID != want.id {
					t.Errorf("item %d observed id = %q, want %q", i, item.observed.ID, want.id)
				}
			}
		})
	}
}

func TestReconcileOnceWrapsSnapshotFailures(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	errStore := errors.New("store boom")
	errRuntime := errors.New("runtime boom")

	tests := []struct {
		name        string
		storeErr    error
		runtimeErr  error
		wantCause   error
		wantMessage string
	}{
		{
			name:        "store snapshot",
			storeErr:    errStore,
			wantCause:   errStore,
			wantMessage: "listing sandbox records",
		},
		{
			name:        "runtime snapshot",
			runtimeErr:  errRuntime,
			wantCause:   errRuntime,
			wantMessage: "listing managed sandboxes",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			runtimeListed := false
			st := storetest.Fake{
				GetSandboxesFn: func(context.Context) ([]*sandbox.Sandbox, error) {
					return nil, tt.storeErr
				},
			}
			rt := runtimetest.Fake{
				ListFn: func(context.Context) ([]runtime.Info, error) {
					runtimeListed = true
					return nil, tt.runtimeErr
				},
			}
			r := NewReconciler(slog.New(slog.DiscardHandler), st, rt)
			r.now = func() time.Time { return now }

			_, err := r.ReconcileOnce(t.Context())

			if !errors.Is(err, tt.wantCause) {
				t.Fatalf("ReconcileOnce error = %v, want errors.Is %v", err, tt.wantCause)
			}
			var reconcileErr *ReconcilerError
			if !errors.As(err, &reconcileErr) {
				t.Fatalf("ReconcileOnce error type = %T, want *ReconcilerError", err)
			}
			if reconcileErr.Op != "reconciler.Reconciler.ReconcileOnce" {
				t.Errorf("ReconcileOnce error Op = %q, want reconciler.Reconciler.ReconcileOnce", reconcileErr.Op)
			}
			if !strings.Contains(err.Error(), tt.wantMessage) {
				t.Errorf("ReconcileOnce error = %q, want message containing %q", err, tt.wantMessage)
			}
			if tt.storeErr != nil && runtimeListed {
				t.Error("runtime.List called after the store snapshot failed")
			}
		})
	}
}

// sbx-a fails permanently (its spec cannot resolve) and sbx-b fails transiently
// (the runtime call errors): the pass must continue past both, and only the
// transient one may ask for a retry.
func TestReconcileOnceLogsActionFailureAndContinues(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	badMemory := "not-a-size"
	sandboxes := []*sandbox.Sandbox{
		{SandboxID: "sbx-b", State: sandbox.Pending, ExpiresAt: now.Add(time.Hour)},
		{SandboxID: "sbx-a", State: sandbox.Pending, ExpiresAt: now.Add(time.Hour), Spec: sandbox.SpecFile{Memory: &badMemory}},
	}

	type transition struct {
		id       string
		from, to sandbox.TaskState
	}
	var created []string
	var updates []transition
	st := storetest.Fake{
		GetSandboxesFn: func(context.Context) ([]*sandbox.Sandbox, error) {
			return sandboxes, nil
		},
		UpdateSandboxStateFn: func(_ context.Context, sandboxID string, from, to sandbox.TaskState, _ string) (*sandbox.Sandbox, error) {
			updates = append(updates, transition{sandboxID, from, to})
			return &sandbox.Sandbox{SandboxID: sandboxID, State: to}, nil
		},
	}
	rt := runtimetest.Fake{
		ListFn: func(context.Context) ([]runtime.Info, error) { return nil, nil },
		CreateFn: func(_ context.Context, sandboxID string, _ runtime.Spec) (runtime.Info, error) {
			created = append(created, sandboxID)
			return runtime.Info{}, errors.New("create boom")
		},
	}
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	r := NewReconciler(logger, st, rt)
	r.now = func() time.Time { return now }

	actions, err := r.ReconcileOnce(t.Context())

	if err != nil {
		t.Fatalf("ReconcileOnce error = %v, want nil for a per-sandbox failure", err)
	}
	if len(actions) != 2 || actions[0] != ActionCreate || actions[1] != ActionCreate {
		t.Errorf("ReconcileOnce actions = %v, want [create create]", actions)
	}
	if len(created) != 1 || created[0] != "sbx-b" {
		t.Errorf("runtime.Create sandbox IDs = %v, want [sbx-b]", created)
	}
	want := []transition{{"sbx-a", sandbox.Pending, sandbox.Failed}}
	if len(updates) != len(want) || updates[0] != want[0] {
		t.Errorf("UpdateSandboxState calls = %+v, want %+v", updates, want)
	}

	gotLogs := logs.String()
	if got := strings.Count(gotLogs, `msg="reconcile action failed; retrying on next pass"`); got != 1 {
		t.Errorf("action failure log count = %d, want 1; logs = %q", got, gotLogs)
	}
	for _, want := range []string{
		"level=WARN",
		"subcomponent=reconciler",
		"sandboxID=sbx-b",
		"action=create",
		"reconciler.Reconciler.handleAction",
		`msg="sandbox spec does not resolve; failing the row" subcomponent=reconciler sandboxID=sbx-a`,
		`msg="reconcile drift repaired" subcomponent=reconciler sandboxID=sbx-a action=create`,
	} {
		if !strings.Contains(gotLogs, want) {
			t.Errorf("logs = %q, want %q", gotLogs, want)
		}
	}
}

func TestReconcileOnceLogsOrphanRepairWithObservedSandboxID(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	st := storetest.Fake{
		GetSandboxesFn: func(context.Context) ([]*sandbox.Sandbox, error) { return nil, nil },
	}
	rt := runtimetest.Fake{
		ListFn: func(context.Context) ([]runtime.Info, error) {
			return []runtime.Info{{ID: "sbx-orphan", State: runtime.StateRunning}}, nil
		},
		DestroyFn: func(context.Context, string) error { return nil },
	}
	var logs bytes.Buffer
	r := NewReconciler(slog.New(slog.NewTextHandler(&logs, nil)), st, rt)
	r.now = func() time.Time { return now }

	if _, err := r.ReconcileOnce(t.Context()); err != nil {
		t.Fatalf("ReconcileOnce error = %v, want nil", err)
	}

	gotLogs := logs.String()
	for _, want := range []string{
		`msg="reconcile drift repaired"`,
		"sandboxID=sbx-orphan",
		"action=destroy_orphan",
	} {
		if !strings.Contains(gotLogs, want) {
			t.Errorf("logs = %q, want %q", gotLogs, want)
		}
	}
}

// Cases assert which side effects each action performs — runtime calls and the
// (from, to) of the guarded write — per the contract comment above handleAction.
func TestHandleAction(t *testing.T) {
	now := time.Date(2026, 8, 2, 12, 0, 0, 0, time.UTC)
	live := now.Add(time.Hour)

	sbx := func(state sandbox.TaskState) *sandbox.Sandbox {
		return &sandbox.Sandbox{SandboxID: "sbx-1", State: state, ExpiresAt: live}
	}
	info := func(state runtime.State) *runtime.Info {
		return &runtime.Info{ID: "sbx-1", State: state}
	}
	badMemory := "not-a-size"
	tinyMemory := "1m"

	errCreate := errors.New("create boom")
	errUpdate := errors.New("update boom")
	errDestroy := errors.New("destroy boom")

	type transition struct{ from, to sandbox.TaskState }

	tests := []struct {
		name        string
		action      ReconcileAction
		desired     *sandbox.Sandbox
		observed    *runtime.Info
		createErr   error
		destroyErr  error
		updateErr   error
		wantErrIs   []error
		wantCreate  bool
		wantDestroy bool
		wantUpdate  *transition
	}{
		{
			name:       "create converges a pending row and records running",
			action:     ActionCreate,
			desired:    sbx(sandbox.Pending),
			wantCreate: true,
			wantUpdate: &transition{sandbox.Pending, sandbox.Running},
		},
		{
			// Resolution is deterministic, so a retry would fail forever: the row
			// is failed instead of the action being reported as transient.
			name:       "create with an unresolvable spec fails the row without a runtime call",
			action:     ActionCreate,
			desired:    &sandbox.Sandbox{SandboxID: "sbx-1", State: sandbox.Pending, Spec: sandbox.SpecFile{Memory: &badMemory}},
			wantUpdate: &transition{sandbox.Pending, sandbox.Failed},
		},
		{
			name:       "create with limits below the floor fails the row without a runtime call",
			action:     ActionCreate,
			desired:    &sandbox.Sandbox{SandboxID: "sbx-1", State: sandbox.Pending, Spec: sandbox.SpecFile{Memory: &tinyMemory}},
			wantUpdate: &transition{sandbox.Pending, sandbox.Failed},
		},
		{
			name:       "failing an unresolvable row is retried when the write itself fails",
			action:     ActionCreate,
			desired:    &sandbox.Sandbox{SandboxID: "sbx-1", State: sandbox.Pending, Spec: sandbox.SpecFile{Memory: &badMemory}},
			updateErr:  errUpdate,
			wantErrIs:  []error{errUpdate},
			wantUpdate: &transition{sandbox.Pending, sandbox.Failed},
		},
		{
			name:       "failed container create leaves the row pending",
			action:     ActionCreate,
			desired:    sbx(sandbox.Pending),
			createErr:  errCreate,
			wantErrIs:  []error{errCreate},
			wantCreate: true,
		},
		{
			name:        "create losing the write-back race destroys the container it made",
			action:      ActionCreate,
			desired:     sbx(sandbox.Pending),
			updateErr:   sandbox.ErrInvalidStateTransition,
			wantErrIs:   []error{sandbox.ErrInvalidStateTransition},
			wantCreate:  true,
			wantDestroy: true,
			wantUpdate:  &transition{sandbox.Pending, sandbox.Running},
		},
		{
			name:        "create write-back and compensation both failing reports both",
			action:      ActionCreate,
			desired:     sbx(sandbox.Pending),
			updateErr:   errUpdate,
			destroyErr:  errDestroy,
			wantErrIs:   []error{errUpdate, errDestroy},
			wantCreate:  true,
			wantDestroy: true,
			wantUpdate:  &transition{sandbox.Pending, sandbox.Running},
		},
		{
			name:        "destroy of a stopping row's container completes the stop",
			action:      ActionDestroy,
			desired:     sbx(sandbox.Stopping),
			observed:    info(runtime.StateRunning),
			wantDestroy: true,
			wantUpdate:  &transition{sandbox.Stopping, sandbox.Stopped},
		},
		{
			name:        "destroy of a stopped row's container writes nothing to the DB",
			action:      ActionDestroy,
			desired:     sbx(sandbox.Stopped),
			observed:    info(runtime.StateRunning),
			wantDestroy: true,
		},
		{
			name:        "destroy of a failed row's container writes nothing to the DB",
			action:      ActionDestroy,
			desired:     sbx(sandbox.Failed),
			observed:    info(runtime.StateStopped),
			wantDestroy: true,
		},
		{
			name:        "orphan destroy touches only the runtime",
			action:      ActionDestroyOrphan,
			observed:    info(runtime.StateRunning),
			wantDestroy: true,
		},
		{
			name:       "reap of an expired pending row with no container only transitions the row",
			action:     ActionReap,
			desired:    sbx(sandbox.Pending),
			wantUpdate: &transition{sandbox.Pending, sandbox.Failed},
		},
		{
			name:        "reap of an expired running container destroys it and records failure",
			action:      ActionReap,
			desired:     sbx(sandbox.Running),
			observed:    info(runtime.StateRunning),
			wantDestroy: true,
			wantUpdate:  &transition{sandbox.Running, sandbox.Failed},
		},
		{
			name:        "reap of an expired stopping row lands on stopped",
			action:      ActionReap,
			desired:     sbx(sandbox.Stopping),
			observed:    info(runtime.StateRunning),
			wantDestroy: true,
			wantUpdate:  &transition{sandbox.Stopping, sandbox.Stopped},
		},
		{
			name:       "vanished container marks a running row failed without runtime calls",
			action:     ActionMarkFailed,
			desired:    sbx(sandbox.Running),
			wantUpdate: &transition{sandbox.Running, sandbox.Failed},
		},
		{
			name:       "exited container under a pending row is marked failed from pending",
			action:     ActionMarkFailed,
			desired:    sbx(sandbox.Pending),
			observed:   info(runtime.StateStopped),
			wantUpdate: &transition{sandbox.Pending, sandbox.Failed},
		},
		{
			name:       "mark stopped records stopping to stopped without runtime calls",
			action:     ActionMarkStopped,
			desired:    sbx(sandbox.Stopping),
			wantUpdate: &transition{sandbox.Stopping, sandbox.Stopped},
		},
		{
			name:       "mark running adopts the container without recreating it",
			action:     ActionMarkRunning,
			desired:    sbx(sandbox.Pending),
			observed:   info(runtime.StateRunning),
			wantUpdate: &transition{sandbox.Pending, sandbox.Running},
		},
		{
			name:       "mark running whose row left pending reports the lost guard",
			action:     ActionMarkRunning,
			desired:    sbx(sandbox.Pending),
			observed:   info(runtime.StateRunning),
			updateErr:  sandbox.ErrInvalidStateTransition,
			wantErrIs:  []error{sandbox.ErrInvalidStateTransition},
			wantUpdate: &transition{sandbox.Pending, sandbox.Running},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var created, destroyed bool
			var gotUpdate *transition

			rt := runtimetest.Fake{
				CreateFn: func(_ context.Context, id string, _ runtime.Spec) (runtime.Info, error) {
					created = true
					return runtime.Info{ID: id, State: runtime.StateRunning}, tt.createErr
				},
				DestroyFn: func(_ context.Context, id string) error {
					destroyed = true
					return tt.destroyErr
				},
			}
			st := storetest.Fake{
				UpdateSandboxStateFn: func(_ context.Context, id string, from, to sandbox.TaskState, reason string) (*sandbox.Sandbox, error) {
					gotUpdate = &transition{from, to}
					if reason == "" {
						t.Error("UpdateSandboxState called with an empty reason; every event needs one")
					}
					if tt.updateErr != nil {
						return nil, tt.updateErr
					}
					return &sandbox.Sandbox{SandboxID: id, State: to}, nil
				},
			}
			r := &Reconciler{
				logger:  slog.New(slog.DiscardHandler),
				store:   st,
				runtime: rt,
				now:     func() time.Time { return now },
			}

			err := r.handleAction(context.Background(), tt.action, tt.desired, tt.observed)

			wantErr := len(tt.wantErrIs) > 0
			if (err != nil) != wantErr {
				t.Fatalf("handleAction(%s) error = %v, wantErr %t", tt.action, err, wantErr)
			}
			for _, target := range tt.wantErrIs {
				if !errors.Is(err, target) {
					t.Errorf("handleAction(%s) error = %v, want errors.Is %v", tt.action, err, target)
				}
			}
			if created != tt.wantCreate {
				t.Errorf("runtime.Create called = %t, want %t", created, tt.wantCreate)
			}
			if destroyed != tt.wantDestroy {
				t.Errorf("runtime.Destroy called = %t, want %t", destroyed, tt.wantDestroy)
			}
			switch {
			case tt.wantUpdate == nil && gotUpdate != nil:
				t.Errorf("UpdateSandboxState called with %+v, want no DB write", *gotUpdate)
			case tt.wantUpdate != nil && gotUpdate == nil:
				t.Errorf("UpdateSandboxState not called, want %+v", *tt.wantUpdate)
			case tt.wantUpdate != nil && *gotUpdate != *tt.wantUpdate:
				t.Errorf("UpdateSandboxState transition = %+v, want %+v", *gotUpdate, *tt.wantUpdate)
			}
		})
	}
}

func TestHandleActionRejectsInvalidDispatch(t *testing.T) {
	tests := []struct {
		name     string
		action   ReconcileAction
		desired  *sandbox.Sandbox
		observed *runtime.Info
		want     string
	}{
		{
			name:     "create without row",
			action:   ActionCreate,
			observed: &runtime.Info{ID: "sbx-orphan", State: runtime.StateRunning},
			want:     "create dispatched without a sandbox row",
		},
		{
			name:   "mark running without row",
			action: ActionMarkRunning,
			want:   "mark_running dispatched without a sandbox row",
		},
		{
			name:    "orphan destroy without a runtime sandbox",
			action:  ActionDestroyOrphan,
			desired: &sandbox.Sandbox{SandboxID: "sbx-1", State: sandbox.Running},
			want:    "destroy_orphan dispatched without a runtime sandbox",
		},
		{
			name:    "converged action is never dispatched",
			action:  ActionNone,
			desired: &sandbox.Sandbox{SandboxID: "sbx-1", State: sandbox.Running},
			want:    `unsupported reconcile action ""`,
		},
		{
			name:    "unsupported action",
			action:  ReconcileAction("unsupported"),
			desired: &sandbox.Sandbox{SandboxID: "sbx-1", State: sandbox.Running},
			want:    `unsupported reconcile action "unsupported"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &Reconciler{}

			err := r.handleAction(t.Context(), tt.action, tt.desired, tt.observed)

			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("handleAction error = %v, want message containing %q", err, tt.want)
			}
			var reconcileErr *ReconcilerError
			if !errors.As(err, &reconcileErr) {
				t.Errorf("handleAction error type = %T, want *ReconcilerError", err)
			}
		})
	}
}

// The tests below run against the real store: what they exercise is the
// guarded SQL and the event rows it commits, which a store fake would only
// re-implement.

func newTestStore(t *testing.T) *sqlite.Store {
	t.Helper()

	st, err := sqlite.New(t.Context(), ":memory:", "", slog.New(slog.DiscardHandler))
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

func seedSandbox(t *testing.T, ctx context.Context, st store.Store, key string) *sandbox.Sandbox {
	t.Helper()

	image := "alpine:3.20"
	sbx, err := st.CreateSandbox(ctx, key, sandbox.SpecFile{Image: &image}, storetest.TestExpiry())
	if err != nil {
		t.Fatalf("CreateSandbox(%q) error = %v, want nil", key, err)
	}
	return sbx
}

// containerWorld is the runtime side of a converging system: what one pass
// creates, the next pass observes. Callers drive it from a single goroutine.
type containerWorld struct {
	states map[string]runtime.State
}

func newContainerWorld(running ...string) *containerWorld {
	w := &containerWorld{states: map[string]runtime.State{}}
	for _, id := range running {
		w.states[id] = runtime.StateRunning
	}
	return w
}

func (w *containerWorld) list() []runtime.Info {
	infoObjs := make([]runtime.Info, 0, len(w.states))
	for id, state := range w.states {
		infoObjs = append(infoObjs, runtime.Info{ID: id, State: state})
	}
	return infoObjs
}

func reconcilePass(t *testing.T, ctx context.Context, r *Reconciler, want ...ReconcileAction) {
	t.Helper()

	got, err := r.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("ReconcileOnce() error = %v, want nil", err)
	}
	if !slices.Equal(got, want) {
		t.Fatalf("ReconcileOnce() = %v, want %v", got, want)
	}
}

// The lost update from plan 06, driven deterministically: the pass reads a
// pending row, the row leaves pending while the (blocked) create runs, and the
// guarded write-back must match zero rows instead of resurrecting the sandbox.
// -race is silent here — the interleaving is between two valid SQL statements —
// so the fake create blocking on a channel is what makes it reproducible.
func TestReconcilerDoesNotResurrectDestroyedSandbox(t *testing.T) {
	tests := []struct {
		name string
		// duringCreate runs on the test goroutine while runtime.Create is blocked.
		duringCreate func(t *testing.T, ctx context.Context, st store.Store, c *control.Control, sandboxID string)
		wantState    sandbox.TaskState
	}{
		{
			// The state machine has no pending -> stopping edge, so the API's
			// delete is reachable only once the row is running.
			name: "api destroy during create leaves the row stopping",
			duringCreate: func(t *testing.T, ctx context.Context, st store.Store, c *control.Control, sandboxID string) {
				if _, err := st.UpdateSandboxState(ctx, sandboxID, sandbox.Pending, sandbox.Running, "raced write-back"); err != nil {
					t.Fatalf("UpdateSandboxState(pending, running) error = %v, want nil", err)
				}
				if err := c.DestroySandbox(ctx, sandboxID); err != nil {
					t.Fatalf("DestroySandbox() error = %v, want nil", err)
				}
			},
			wantState: sandbox.Stopping,
		},
		{
			name: "row failed during create stays failed",
			duringCreate: func(t *testing.T, ctx context.Context, st store.Store, c *control.Control, sandboxID string) {
				if _, err := st.UpdateSandboxState(ctx, sandboxID, sandbox.Pending, sandbox.Failed, "failed during create"); err != nil {
					t.Fatalf("UpdateSandboxState(pending, failed) error = %v, want nil", err)
				}
			},
			wantState: sandbox.Failed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := t.Context()
			st := newTestStore(t)
			sbx := seedSandbox(t, ctx, st, "resurrect")

			createStarted := make(chan struct{})
			releaseCreate := make(chan struct{})
			var destroyed []string
			rt := runtimetest.Fake{
				ListFn: func(context.Context) ([]runtime.Info, error) { return nil, nil },
				CreateFn: func(_ context.Context, sandboxID string, _ runtime.Spec) (runtime.Info, error) {
					close(createStarted)
					<-releaseCreate
					return runtime.Info{ID: sandboxID, State: runtime.StateRunning}, nil
				},
				DestroyFn: func(_ context.Context, sandboxID string) error {
					destroyed = append(destroyed, sandboxID)
					return nil
				},
			}
			// Only the reconciler goroutine writes to logs: control keeps the
			// discard logger so the buffer has a single writer.
			var logs bytes.Buffer
			r := NewReconciler(slog.New(slog.NewTextHandler(&logs, nil)), st, rt)
			r.now = func() time.Time { return storetest.TestExpiry().Add(-time.Hour) }
			c := control.New(slog.New(slog.DiscardHandler), st, rt)

			type pass struct {
				actions []ReconcileAction
				err     error
			}
			done := make(chan pass, 1)
			go func() {
				actions, err := r.ReconcileOnce(ctx)
				done <- pass{actions, err}
			}()

			<-createStarted
			tt.duringCreate(t, ctx, st, c, sbx.SandboxID)
			close(releaseCreate)
			got := <-done

			if got.err != nil {
				t.Fatalf("ReconcileOnce() error = %v, want nil: a lost write-back is a per-sandbox failure", got.err)
			}
			if len(got.actions) != 1 || got.actions[0] != ActionCreate {
				t.Errorf("ReconcileOnce() actions = %v, want [create]", got.actions)
			}
			if len(destroyed) != 1 || destroyed[0] != sbx.SandboxID {
				t.Errorf("runtime.Destroy calls = %v, want the container the pass just created to be compensated", destroyed)
			}

			gotLogs := logs.String()
			for _, want := range []string{
				`msg="reconcile action failed; retrying on next pass"`,
				"sandboxID=" + sbx.SandboxID,
				"action=create",
				// The guarded UPDATE matched zero rows.
				sandbox.ErrInvalidStateTransition.Error(),
			} {
				if !strings.Contains(gotLogs, want) {
					t.Errorf("logs = %q, want %q", gotLogs, want)
				}
			}

			persisted, err := st.GetSandbox(ctx, sbx.SandboxID)
			if err != nil {
				t.Fatalf("GetSandbox(%s) error = %v, want nil", sbx.SandboxID, err)
			}
			if persisted.State != tt.wantState {
				t.Errorf("state after the lost write-back = %q, want %q", persisted.State, tt.wantState)
			}

			evts, err := st.GetSandboxEvents(ctx, sbx.SandboxID)
			if err != nil {
				t.Fatalf("GetSandboxEvents(%s) error = %v, want nil", sbx.SandboxID, err)
			}
			if last := evts[len(evts)-1]; last.ToState != tt.wantState {
				t.Errorf("last event to state = %q, want %q: a write that did not happen appends no event", last.ToState, tt.wantState)
			}
		})
	}
}

// The invariant the append-only log exists for, over the whole lifecycle rather
// than one store call: the API records intent, the reconciler converges, and
// replaying the events lands on the state the row reports.
func TestEveryTransitionEmitsEvent(t *testing.T) {
	ctx := t.Context()
	logger := slog.New(slog.DiscardHandler)
	st := newTestStore(t)

	world := newContainerWorld()
	rt := runtimetest.Fake{
		ListFn: func(context.Context) ([]runtime.Info, error) { return world.list(), nil },
		CreateFn: func(_ context.Context, sandboxID string, _ runtime.Spec) (runtime.Info, error) {
			world.states[sandboxID] = runtime.StateRunning
			return runtime.Info{ID: sandboxID, State: runtime.StateRunning}, nil
		},
		DestroyFn: func(_ context.Context, sandboxID string) error {
			delete(world.states, sandboxID)
			return nil
		},
	}
	c := control.New(logger, st, rt)
	r := NewReconciler(logger, st, rt)

	image := "alpine:3.20"
	sbx, err := c.CreateSandbox(ctx, "lifecycle", sandbox.SpecFile{Image: &image}, 0)
	if err != nil {
		t.Fatalf("CreateSandbox() error = %v, want nil", err)
	}
	reconcilePass(t, ctx, r, ActionCreate)
	if err := c.DestroySandbox(ctx, sbx.SandboxID); err != nil {
		t.Fatalf("DestroySandbox() error = %v, want nil", err)
	}
	reconcilePass(t, ctx, r, ActionDestroy)

	final, err := st.GetSandbox(ctx, sbx.SandboxID)
	if err != nil {
		t.Fatalf("GetSandbox(%s) error = %v, want nil", sbx.SandboxID, err)
	}
	if final.State != sandbox.Stopped {
		t.Fatalf("state after the full lifecycle = %q, want %q", final.State, sandbox.Stopped)
	}

	got, err := st.GetSandboxEvents(ctx, sbx.SandboxID)
	if err != nil {
		t.Fatalf("GetSandboxEvents(%s) error = %v, want nil", sbx.SandboxID, err)
	}
	if len(got) != 4 {
		t.Fatalf("GetSandboxEvents(%s) returned %d events, want the create plus 3 lifecycle transitions", sbx.SandboxID, len(got))
	}

	// The zero state is where a replay starts: the create event comes from nothing.
	var replayed sandbox.TaskState
	for i, e := range got {
		if e.FromState != replayed {
			t.Fatalf("event %d from state = %q, want the previous event's to state %q: the log has a hole", i, e.FromState, replayed)
		}
		if e.Reason == "" {
			t.Errorf("event %d has no reason, want the transition's", i)
		}
		replayed = e.ToState
	}
	if replayed != final.State {
		t.Errorf("replayed state = %q, want the sandbox's current %q", replayed, final.State)
	}
}

// The property that lets the pass run on a dumb ticker: once the world is
// converged, a pass observes no gap and therefore performs no I/O.
func TestReconcileOncePassIsIdempotent(t *testing.T) {
	ctx := t.Context()
	st := newTestStore(t)
	sbx := seedSandbox(t, ctx, st, "idempotent")

	world := newContainerWorld("sbx_orphan")
	converged := false
	rt := runtimetest.Fake{
		ListFn: func(context.Context) ([]runtime.Info, error) { return world.list(), nil },
		CreateFn: func(_ context.Context, sandboxID string, _ runtime.Spec) (runtime.Info, error) {
			if converged {
				t.Errorf("runtime.Create(%s) called on a converged pass, want no runtime writes", sandboxID)
			}
			world.states[sandboxID] = runtime.StateRunning
			return runtime.Info{ID: sandboxID, State: runtime.StateRunning}, nil
		},
		DestroyFn: func(_ context.Context, sandboxID string) error {
			if converged {
				t.Errorf("runtime.Destroy(%s) called on a converged pass, want no runtime writes", sandboxID)
			}
			delete(world.states, sandboxID)
			return nil
		},
	}
	r := NewReconciler(slog.New(slog.DiscardHandler), st, rt)
	r.now = func() time.Time { return storetest.TestExpiry().Add(-time.Hour) }

	// Two drifts: a pending row with no container, and a labeled container with
	// no row. Pass order follows sandbox id, which the store mints.
	got, err := r.ReconcileOnce(ctx)
	if err != nil {
		t.Fatalf("first ReconcileOnce() error = %v, want nil", err)
	}
	slices.Sort(got)
	if want := []ReconcileAction{ActionCreate, ActionDestroyOrphan}; !slices.Equal(got, want) {
		t.Fatalf("first ReconcileOnce() = %v, want %v", got, want)
	}

	before, err := st.GetSandboxEvents(ctx, sbx.SandboxID)
	if err != nil {
		t.Fatalf("GetSandboxEvents(%s) error = %v, want nil", sbx.SandboxID, err)
	}

	converged = true
	reconcilePass(t, ctx, r)

	after, err := st.GetSandboxEvents(ctx, sbx.SandboxID)
	if err != nil {
		t.Fatalf("GetSandboxEvents(%s) error = %v, want nil", sbx.SandboxID, err)
	}
	if len(after) != len(before) {
		t.Errorf("events after the converged pass = %d, want the %d the first pass left: a no-op pass writes nothing", len(after), len(before))
	}
}
