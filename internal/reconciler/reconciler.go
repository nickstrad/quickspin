package reconciler

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/nickstrad/quickspin/internal/sandbox"
	"github.com/nickstrad/quickspin/internal/store"
)

type Reconciler struct {
	mu      sync.Mutex
	logger  *slog.Logger
	store   store.Store
	runtime runtime.Runtime
	now     func() time.Time
}

// time.NewTicker panics on a non-positive interval, so an unset interval must
// not reach the reconcile loop.
const defaultInterval = 15 * time.Second

type ReconcileAction string

const (
	// ActionNone means the pair is converged; ReconcileOnce must not count it,
	// or an idle pass would never report as a no-op.
	ActionNone          ReconcileAction = ""
	ActionCreate        ReconcileAction = "create"         // pending row, no container
	ActionDestroy       ReconcileAction = "destroy"        // terminal/stopping row still has a container
	ActionDestroyOrphan ReconcileAction = "destroy_orphan" // labeled container with no row
	ActionReap          ReconcileAction = "reap"           // past ExpiresAt: destroy + transition
	ActionMarkFailed    ReconcileAction = "mark_failed"    // container vanished or exited under a running row
	ActionMarkStopped   ReconcileAction = "mark_stopped"   // stopping row, container already gone: DB-only transition
	ActionMarkRunning   ReconcileAction = "mark_running"   // pending row, container already up: finish a lost write-back
)

func NewReconciler(logger *slog.Logger, store store.Store, runtime runtime.Runtime) *Reconciler {
	return &Reconciler{
		logger:  logger.With("subcomponent", "reconciler"),
		store:   store,
		runtime: runtime,
		now:     time.Now,
	}
}

func (r *Reconciler) ReconcileOnce(ctx context.Context) ([]ReconcileAction, error) {
	const op = "reconciler.Reconciler.ReconcileOnce"

	if !r.mu.TryLock() {
		return nil, ErrPassInFlight
	}
	defer r.mu.Unlock()
	r.logger.DebugContext(ctx, "reconcile pass started")

	sbxs, err := r.store.GetSandboxes(ctx)
	if err != nil {
		return nil, Wrap(op, "listing sandbox records", err)
	}

	infoObjs, err := r.runtime.List(ctx)
	if err != nil {
		return nil, Wrap(op, "listing managed sandboxes", err)
	}

	actions := []ReconcileAction{}
	now := r.now()
	for _, item := range pairSnapshots(sbxs, infoObjs) {

		action := decideReconcileAction(item.desired, item.observed, now)
		if action == ActionNone {
			continue
		}
		err := r.handleAction(ctx, action, item.desired, item.observed)
		if err != nil {
			r.logger.WarnContext(ctx, "reconcile action failed; retrying on next pass",
				"sandboxID", item.id(),
				"action", action,
				"err", err,
			)
		} else {
			r.logger.InfoContext(ctx, "reconcile drift repaired",
				"sandboxID", item.id(),
				"action", action,
			)
		}
		actions = append(actions, action)
	}

	r.logger.DebugContext(ctx, "reconcile pass completed", "actions", len(actions))
	return actions, nil
}

// A row, the container observed for it, or either one alone.
type reconcileItem struct {
	desired  *sandbox.Sandbox
	observed *runtime.Info
}

func (it reconcileItem) id() string {
	if it.desired != nil {
		return it.desired.SandboxID
	}
	return it.observed.ID
}

// pairSnapshots outer-joins the two snapshots on sandbox ID: every row appears
// once with its container or nil, and every container without a row appears as
// an orphan. Sorted by ID so a pass visits sandboxes in the same order whatever
// order the store and runtime listed them in.
func pairSnapshots(sbxs []*sandbox.Sandbox, infoObjs []runtime.Info) []reconcileItem {
	infoObjSbxIDToInfoObj := map[string]*runtime.Info{}
	for i := range infoObjs {
		infoObjSbxIDToInfoObj[infoObjs[i].ID] = &infoObjs[i]
	}

	reconcileItems := make([]reconcileItem, 0, len(sbxs)+len(infoObjs))

	for _, sbx := range sbxs {
		reconcileItems = append(reconcileItems, reconcileItem{
			desired:  sbx,
			observed: infoObjSbxIDToInfoObj[sbx.SandboxID],
		})
		delete(infoObjSbxIDToInfoObj, sbx.SandboxID)
	}

	for _, iObj := range infoObjSbxIDToInfoObj {
		reconcileItems = append(reconcileItems, reconcileItem{
			observed: iObj,
		})
	}

	slices.SortFunc(reconcileItems, func(a, b reconcileItem) int {
		return cmp.Compare(a.id(), b.id())
	})

	return reconcileItems
}

/*
What each action does — the I/O side of the decision table above decideReconcileAction.
Every DB write is guarded (WHERE restates the state the pass read) and appends its
event row in the same transaction.

	ActionNone          — never dispatched (ReconcileOnce filters it out); reaching
	                      here is a caller bug and reports as an unsupported action
	ActionCreate        — runtime.Create for the pending row, then guarded UPDATE
	                      pending -> running; zero rows affected means the row changed
	                      underneath us: destroy the container we just made. A spec
	                      that does not resolve is permanent, not transient: no
	                      runtime call, guarded UPDATE pending -> failed instead, and
	                      the action succeeds — the drift is repaired by failing the row
	ActionDestroy       — runtime.Destroy the leftover container for a stopping or
	                      terminal row; for stopping, then guarded UPDATE -> stopped
	ActionDestroyOrphan — runtime.Destroy a labeled container with no row; no DB write
	ActionReap          — expired: runtime.Destroy if a container exists, then guarded
	                      UPDATE to the terminal state
	ActionMarkFailed    — no runtime call (container already gone or exited); guarded
	                      UPDATE running -> failed
	ActionMarkStopped   — no runtime call; guarded UPDATE stopping -> stopped
	ActionMarkRunning   — no runtime call (the container is already up); guarded
	                      UPDATE pending -> running
*/
func (r *Reconciler) handleAction(ctx context.Context, action ReconcileAction, sbx *sandbox.Sandbox, infoObj *runtime.Info) error {
	const op = "reconciler.Reconciler.handleAction"

	sbxID := ""
	if sbx != nil {
		sbxID = sbx.SandboxID
	} else if infoObj != nil {
		sbxID = infoObj.ID
	}

	if action == ActionDestroyOrphan {
		if infoObj == nil || sbxID == "" {
			return E(op, fmt.Sprintf("%s dispatched without a runtime sandbox", action), nil)
		}
	} else if sbx == nil || sbxID == "" {
		return E(op, fmt.Sprintf("%s dispatched without a sandbox row", action), nil)
	}

	switch action {
	case ActionCreate:
		resolved, err := sbx.Spec.ResolveValidated()
		if err != nil {
			// Pure computation over the stored spec, so it fails identically on
			// every future pass; the event reason keeps only the verdict, so the
			// error itself is logged here or it is lost.
			r.logger.WarnContext(ctx, "sandbox spec does not resolve; failing the row",
				"sandboxID", sbxID, "err", err)
			return r.mark(ctx, sbx, sandbox.Failed,
				"reconciler failed sandbox: spec does not resolve",
				fmt.Sprintf("recording sandbox %s as failed after its spec did not resolve", sbxID))
		}
		if _, err := r.runtime.Create(ctx, sbxID, resolved); err != nil {
			return Wrap(op, fmt.Sprintf("creating sandbox %s", sbxID), err)
		}
		_, err = r.store.UpdateSandboxState(ctx, sbxID, sandbox.Pending, sandbox.Running, "reconciler created container", sbx.VersionID)
		if err == nil {
			return nil
		}
		// The write-back did not commit — either the guard missed (the row left
		// pending during the seconds of Create) or the store failed. Both leave
		// the row not owning this container, so undo the side effect; destroy is
		// idempotent and returns the world to a state the next pass converges from.
		if destroyErr := r.runtime.Destroy(ctx, sbxID); destroyErr != nil {
			return Wrap(op, fmt.Sprintf("recording sandbox %s as running failed and removing its container also failed", sbxID), errors.Join(err, destroyErr))
		}
		return Wrap(op, fmt.Sprintf("recording sandbox %s as running failed; container removed", sbxID), err)
	case ActionDestroy:
		if err := r.runtime.Destroy(ctx, sbxID); err != nil {
			return Wrap(op, fmt.Sprintf("destroying leftover container for sandbox %s", sbxID), err)
		}
		// A terminal row is already recorded; only a stopping row's destroy
		// completes a transition.
		if sbx.State == sandbox.Stopping {
			return r.mark(ctx, sbx, sandbox.Stopped,
				"reconciler destroyed container",
				fmt.Sprintf("recording sandbox %s as stopped", sbxID))
		}
		return nil
	case ActionDestroyOrphan:
		if err := r.runtime.Destroy(ctx, sbxID); err != nil {
			return Wrap(op, fmt.Sprintf("destroying orphan sandbox %s", sbxID), err)
		}
		return nil
	case ActionReap:
		if infoObj != nil {
			if err := r.runtime.Destroy(ctx, sbxID); err != nil {
				return Wrap(op, fmt.Sprintf("destroying expired sandbox %s", sbxID), err)
			}
		}
		// The state machine has no expired->stopped edge from pending or
		// running, so reap lands on the legal terminal for each source state.
		to := sandbox.Failed
		if sbx.State == sandbox.Stopping {
			to = sandbox.Stopped
		}
		return r.mark(ctx, sbx, to,
			"reconciler reaped expired sandbox",
			fmt.Sprintf("recording reaped sandbox %s as %s", sbxID, to))
	case ActionMarkFailed:
		return r.mark(ctx, sbx, sandbox.Failed,
			"reconciler marked sandbox failed",
			fmt.Sprintf("recording sandbox %s as failed", sbxID))
	case ActionMarkStopped:
		return r.mark(ctx, sbx, sandbox.Stopped,
			"reconciler marked sandbox stopped",
			fmt.Sprintf("recording sandbox %s as stopped", sbxID))
	case ActionMarkRunning:
		return r.mark(ctx, sbx, sandbox.Running,
			"reconciler adopted existing container",
			fmt.Sprintf("recording sandbox %s as running", sbxID))

	default:
		return E(op, fmt.Sprintf("unsupported reconcile action %q", action), nil)
	}
}

// mark performs the guarded transition every DB-writing action ends in: reason
// is recorded on the event, describing names the write in the wrapped error.
// It reports handleAction as the op so a failure points at the dispatch site
// rather than at this helper.
func (r *Reconciler) mark(ctx context.Context, sbx *sandbox.Sandbox, to sandbox.TaskState, reason, describing string) error {
	const op = "reconciler.Reconciler.handleAction"

	if _, err := r.store.UpdateSandboxState(ctx, sbx.SandboxID, sbx.State, to, reason, sbx.VersionID); err != nil {
		return Wrap(op, describing, err)
	}
	return nil
}

/*
Decision table: desired row state (rows, nil = no DB row) x observed container
state (columns, nil = no container). Expiry is checked first: any non-terminal
row past ExpiresAt -> ActionReap, regardless of the cell below.

	db \ runtime | nil               | running             | stopped
	-------------+-------------------+---------------------+---------------------
	nil          | (no pairing)      | ActionDestroyOrphan | ActionDestroyOrphan  orphan: DB is authoritative
	pending      | ActionCreate      | ActionMarkRunning   | ActionMarkFailed     running = crash after create, before write-back
	running      | ActionMarkFailed  | ActionNone          | ActionMarkFailed     container vanished or exited underneath us
	stopping     | ActionMarkStopped | ActionDestroy       | ActionDestroy        finish the stop, then transition to stopped
	stopped      | ActionNone        | ActionDestroy       | ActionDestroy        terminal row: any leftover container is garbage
	failed       | ActionNone        | ActionDestroy       | ActionDestroy        terminal row: any leftover container is garbage

How to read it: desired picks the row (nil desired = first row), observed picks
the column (nil observed = first column). One example call per table row:

	decideReconcileAction(nil,                       &Info{State: StateRunning}, now) = ActionDestroyOrphan  // row nil,      col running
	decideReconcileAction(&Sandbox{State: Pending},  nil,                        now) = ActionCreate         // row pending,  col nil
	decideReconcileAction(&Sandbox{State: Running},  nil,                        now) = ActionMarkFailed     // row running,  col nil
	decideReconcileAction(&Sandbox{State: Stopping}, &Info{State: StateRunning}, now) = ActionDestroy        // row stopping, col running
	decideReconcileAction(&Sandbox{State: Stopped},  &Info{State: StateStopped}, now) = ActionDestroy        // row stopped,  col stopped
	decideReconcileAction(&Sandbox{State: Failed},   nil,                        now) = ActionNone           // row failed,   col nil

	// expiry override: ExpiresAt in the past wins before any cell is looked up
	decideReconcileAction(&Sandbox{State: Running, ExpiresAt: past}, &Info{State: StateRunning}, now) = ActionReap

Adopting a container under a pending row (ActionMarkRunning) assumes one reconciler:
a second one mid-Create would find its own write-back guard missing and destroy the
container this pass just adopted. Leases are what make that safe to relax.
*/
func decideReconcileAction(desired *sandbox.Sandbox, observed *runtime.Info, now time.Time) ReconcileAction {

	if desired == nil {
		if observed == nil {
			return ActionNone
		}
		return ActionDestroyOrphan
	}

	terminal := desired.State == sandbox.Stopped || desired.State == sandbox.Failed
	if !terminal && now.After(desired.ExpiresAt) {
		return ActionReap
	}

	switch desired.State {
	case sandbox.Pending:
		switch {
		case observed == nil:
			return ActionCreate
		case observed.State == runtime.StateStopped:
			return ActionMarkFailed
		default:
			return ActionMarkRunning
		}
	case sandbox.Running:
		if observed == nil || observed.State == runtime.StateStopped {
			return ActionMarkFailed
		}
		return ActionNone
	case sandbox.Stopping:
		if observed == nil {
			return ActionMarkStopped
		}
		return ActionDestroy
	case sandbox.Stopped, sandbox.Failed:
		if observed == nil {
			return ActionNone
		}
		return ActionDestroy
	}

	return ActionNone
}

func (r *Reconciler) Start(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = defaultInterval
	}
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				r.logger.InfoContext(ctx, "reconciler stopped")
				return
			case <-t.C:
				if _, err := r.ReconcileOnce(ctx); err != nil {
					// This goroutine is the propagation stop, so it records the
					// pass-wide error once and keeps the loop alive.
					if errors.Is(err, ErrPassInFlight) {
						r.logger.DebugContext(ctx, "reconcile pass skipped", "err", err)
					} else {
						r.logger.ErrorContext(ctx, "reconcile pass failed", "err", err)
					}
				}
				t.Reset(interval + time.Duration(rand.Int64N(int64(interval/10))))
			}
		}
	}()
}
