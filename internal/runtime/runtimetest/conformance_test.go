package runtimetest

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/nickstrad/quickspin/internal/runtime"
)

// The suite claims two things a live backend run cannot show: that it polls
// rather than assuming immediate visibility, and that it fails — with cleanup
// still registered — when an implementation misbehaves. A passing Docker run
// proves neither, because a suite that asserted nothing would also pass.

// harnessTimeout is generous enough to survive a loaded machine.
// unobservableTimeout is the one a clause is meant to exhaust, so it uses the
// shortest deadline that still allows several polls — but the same value also
// has to be long enough for the clauses that must succeed in the same run,
// which is why it is milliseconds rather than microseconds. Both stay in the
// millisecond range because
// TestMain shrinks pollInterval — these tests assert that polling happened, never
// how long it took.
const (
	harnessTimeout      = 2 * time.Second
	unobservableTimeout = 20 * time.Millisecond

	// forever is a hide budget no run can exhaust, for the tests that need one
	// operation to stay unobservable rather than merely slow.
	forever = 1 << 30
)

// TestMain shrinks the poll interval for this package only. At the production
// 50ms these three tests spend nearly half a second asleep, in the fast suite
// that exists to stay fast; none of them assert anything about duration.
func TestMain(m *testing.M) {
	pollInterval = time.Millisecond
	os.Exit(m.Run())
}

func TestConformancePassesWhenVisibilityIsDelayed(t *testing.T) {
	// Hiding is what a Kubernetes or cloud-mediated backend does naturally: the
	// create succeeded, but the resource is not yet observable. A suite that
	// asserted immediately would fail here on correct behavior. Both Inspect and
	// List get their own budget so each clause polls through a delay of its own.
	const hide = 3
	rt := &memRuntime{inspect: gate{hide: hide}, list: gate{hide: hide}}
	rec := new(recorder)

	runRecorded(t, rec, rt, harnessTimeout)

	if len(rec.failures) != 0 {
		t.Fatalf("RunConformance failures = %v, want none for a compliant runtime", rec.failures)
	}
	if rt.inspect.seen <= hide {
		t.Errorf("Inspect observations = %d, want more than the %d hidden: the suite must poll", rt.inspect.seen, hide)
	}
	if rt.list.seen <= hide {
		t.Errorf("List observations = %d, want more than the %d hidden: the suite must poll", rt.list.seen, hide)
	}
}

func TestConformanceFailsAndStillCleansUpAnUnobservableCreate(t *testing.T) {
	// A create that never becomes visible is the shape of a real backend bug —
	// a wrong label filter, a namespace mismatch. The suite must call it, and
	// must still remove what the create allocated.
	rt := &memRuntime{inspect: gate{hide: forever}}
	rec := new(recorder)

	runRecorded(t, rec, rt, unobservableTimeout)

	if len(rec.failures) == 0 {
		t.Fatal("RunConformance reported no failure for a runtime whose create is never observable")
	}
	if got := rec.failures[0]; !strings.Contains(got, "must observe the created sandbox") {
		t.Errorf("first failure = %q, want the violated clause named", got)
	}

	if rt.count() != 1 {
		t.Fatalf("sandboxes before cleanup = %d, want the created one still present", rt.count())
	}
	rec.runCleanups()
	if rt.count() != 0 {
		t.Errorf("sandboxes after cleanup = %d, want 0: cleanup must be registered before the assertions", rt.count())
	}
}

func TestConformanceFailsWhenListNeverContainsTheCreatedSandbox(t *testing.T) {
	// A backend whose Inspect works but whose List omits the sandbox is the
	// shape of a filter that lost a label, and it is the one clause a pass-path
	// run cannot prove the suite enforces: List is asked about a sandbox that is
	// already there, so a suite that never checked would look identical.
	rt := &memRuntime{list: gate{hide: forever}}
	rec := new(recorder)

	runRecorded(t, rec, rt, unobservableTimeout)

	if len(rec.failures) == 0 {
		t.Fatal("RunConformance reported no failure for a runtime whose List omits the created sandbox")
	}
	if got := rec.failures[0]; !strings.Contains(got, "List must contain the created sandbox") {
		t.Errorf("first failure = %q, want the List clause named", got)
	}
}

// The conformance suite does not exercise WriteFile, so this pins directly that
// the double keeps the contract's identity sentinels: malformed and unknown ids
// must stay distinguishable, exactly as conformMalformedID demands of real
// backends.
func TestMemRuntimeWriteFileSharesTheIdentitySentinels(t *testing.T) {
	rt := new(memRuntime)
	ctx := context.Background()

	if err := rt.WriteFile(ctx, "not-a-sandbox-id", "/work/main.go", nil, 0o644); !errors.Is(err, runtime.ErrInvalidSandboxID) {
		t.Errorf("WriteFile(malformed id) error = %v, want ErrInvalidSandboxID", err)
	}
	if err := rt.WriteFile(ctx, "sbx_unknown", "/work/main.go", nil, 0o644); !errors.Is(err, runtime.ErrNotFound) {
		t.Errorf("WriteFile(unknown id) error = %v, want ErrNotFound", err)
	}

	created, err := rt.Create(ctx, runtime.Spec{})
	if err != nil {
		t.Fatalf("Create error = %v, want nil", err)
	}
	if err := rt.WriteFile(ctx, created.ID, "/work/main.go", []byte("package main"), 0o644); err != nil {
		t.Errorf("WriteFile(existing sandbox) error = %v, want nil", err)
	}
}

// The identity sentinels come first for reads too: an unknown sandbox is
// ErrNotFound, never the ErrPathNotFound that would send a caller looking for a
// missing file inside a sandbox that does not exist.
func TestMemRuntimeReadFileSharesTheIdentitySentinels(t *testing.T) {
	rt := new(memRuntime)
	ctx := context.Background()

	if _, err := rt.ReadFile(ctx, "not-a-sandbox-id", "/work/main.go"); !errors.Is(err, runtime.ErrInvalidSandboxID) {
		t.Errorf("ReadFile(malformed id) error = %v, want ErrInvalidSandboxID", err)
	}
	if _, err := rt.ReadFile(ctx, "sbx_unknown", "/work/main.go"); !errors.Is(err, runtime.ErrNotFound) {
		t.Errorf("ReadFile(unknown id) error = %v, want ErrNotFound", err)
	}

	created, err := rt.Create(ctx, runtime.Spec{})
	if err != nil {
		t.Fatalf("Create error = %v, want nil", err)
	}
	// The double stores nothing, so a known sandbox turns the answer into a
	// statement about the path.
	if _, err := rt.ReadFile(ctx, created.ID, "/work/main.go"); !errors.Is(err, runtime.ErrPathNotFound) {
		t.Errorf("ReadFile(existing sandbox) error = %v, want ErrPathNotFound", err)
	}
}

func TestPollReportsItsLastObservation(t *testing.T) {
	wantErr := errors.New("daemon unreachable")

	last, lastErr, timedOut := poll(
		2*pollInterval,
		func(context.Context) (string, error) { return "still-pending", wantErr },
		func(string, error) bool { return false },
	)

	if timedOut == nil {
		t.Fatal("poll timedOut = nil, want a timeout for a condition that never settles")
	}
	if !strings.Contains(timedOut.Error(), "attempts") {
		t.Errorf("timeout = %q, want the attempt count for diagnosis", timedOut)
	}
	if last != "still-pending" || !errors.Is(lastErr, wantErr) {
		t.Errorf("poll last = (%q, %v), want the final observation and error", last, lastErr)
	}
}

// runRecorded runs the suite against a recorder. RunConformance's Fatalf must
// abort its caller the way *testing.T's does, so recorder panics and the
// unwinding stops here.
func runRecorded(t *testing.T, rec *recorder, rt runtime.Runtime, observe time.Duration) {
	t.Helper()

	defer func() {
		if r := recover(); r != nil && r != errFatal {
			panic(r)
		}
	}()
	RunConformance(rec, rt, runtime.NewSpec("in-memory", nil, 0.5, 64*1024*1024, 128, false), observe)
}

var errFatal = errors.New("recorder: Fatalf")

// recorder stands in for *testing.T. It holds cleanups rather than running them
// so a test can assert they were registered and then decide when they run.
type recorder struct {
	failures []string
	cleanups []func()
}

func (r *recorder) Helper() {}

func (r *recorder) Errorf(format string, args ...any) {
	r.failures = append(r.failures, fmt.Sprintf(format, args...))
}

func (r *recorder) Fatalf(format string, args ...any) {
	r.Errorf(format, args...)
	panic(errFatal)
}

func (r *recorder) Cleanup(fn func()) { r.cleanups = append(r.cleanups, fn) }

// runCleanups runs in reverse registration order, as testing does.
func (r *recorder) runCleanups() {
	for _, cleanup := range slices.Backward(r.cleanups) {
		cleanup()
	}
}

// memRuntime is the least stateful runtime that can exercise the suite: a map
// plus a knob that delays visibility. It is not a fourth backend — if it grows a
// config surface or tests of its own, cut it back.
//
// The delay is budgeted per operation rather than shared. A single counter lets
// whichever clause the suite reaches first spend the whole budget, so every
// later clause runs against a runtime that is no longer hiding anything and its
// convergence is never actually exercised.
type memRuntime struct {
	mu        sync.Mutex
	sandboxes map[string]runtime.Info

	inspect gate
	list    gate
}

// gate hides an operation for its first hide observations. The budget and the
// tally live in one value so a call site cannot pair one operation's budget with
// another's counter, and so a third gated operation costs one field, not two.
type gate struct {
	hide, seen int
}

func (g *gate) hidden() bool {
	g.seen++
	return g.seen <= g.hide
}

var _ runtime.Runtime = (*memRuntime)(nil)

func (m *memRuntime) Create(_ context.Context, _ runtime.Spec) (runtime.Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.sandboxes == nil {
		m.sandboxes = map[string]runtime.Info{}
	}
	info := runtime.NewInfo("sbx_"+uuid.NewString(), runtime.StateRunning, time.Now().UTC())
	m.sandboxes[info.ID] = info
	return info, nil
}

func (m *memRuntime) Inspect(_ context.Context, id string) (runtime.Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := validateMemID(id); err != nil {
		return runtime.Info{}, err
	}
	// Counted before the lookup rather than short-circuited past it, so the
	// observation tally stays a count of calls and not of hits.
	hidden := m.inspect.hidden()
	info, ok := m.sandboxes[id]
	if !ok || hidden {
		return runtime.Info{}, runtime.ErrNotFound
	}
	return info, nil
}

func (m *memRuntime) List(context.Context) ([]runtime.Info, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.list.hidden() {
		return nil, nil
	}
	infos := make([]runtime.Info, 0, len(m.sandboxes))
	for _, info := range m.sandboxes {
		infos = append(infos, info)
	}
	return infos, nil
}

func (m *memRuntime) Destroy(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := validateMemID(id); err != nil {
		return err
	}
	delete(m.sandboxes, id)
	return nil
}

// Exec exists so memRuntime still satisfies runtime.Runtime. The conformance
// suite does not exercise exec — there is no in-memory analogue of a process —
// so this reports success without running anything, and a suite clause that
// starts depending on exec will see that emptiness rather than a plausible lie.
func (m *memRuntime) Exec(_ context.Context, id string, _ []string, _ runtime.ExecOpts) (runtime.ExecResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := validateMemID(id); err != nil {
		return runtime.ExecResult{}, err
	}
	if _, ok := m.sandboxes[id]; !ok {
		return runtime.ExecResult{}, runtime.ErrNotFound
	}
	return runtime.ExecResult{}, nil
}

// WriteFile exists so memRuntime still satisfies runtime.Runtime. Like Exec,
// there is no in-memory filesystem, so once the sandbox is known it reports
// success without storing anything.
func (m *memRuntime) WriteFile(_ context.Context, id, _ string, _ []byte, _ fs.FileMode) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := validateMemID(id); err != nil {
		return err
	}
	if _, ok := m.sandboxes[id]; !ok {
		return runtime.ErrNotFound
	}
	return nil
}

// ReadFile exists so memRuntime still satisfies runtime.Runtime. WriteFile
// stores nothing, so every path really is absent and ErrPathNotFound is the
// honest report; empty content would claim a file that was never written.
func (m *memRuntime) ReadFile(_ context.Context, id, _ string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := validateMemID(id); err != nil {
		return nil, err
	}
	if _, ok := m.sandboxes[id]; !ok {
		return nil, runtime.ErrNotFound
	}
	return nil, runtime.ErrPathNotFound
}

// validateMemID applies the prefix rule alone. The double deliberately does not
// reimplement the real id format: the suite only needs malformed and
// well-formed-but-unknown to stay distinguishable.
func validateMemID(id string) error {
	if !strings.HasPrefix(id, "sbx_") {
		return runtime.ErrInvalidSandboxID
	}
	return nil
}

func (m *memRuntime) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sandboxes)
}
