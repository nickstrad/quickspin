package runtimetest

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nickstrad/quickspin/internal/runtime"
)

// TB is the part of *testing.T the conformance suite uses. Declaring it rather
// than taking *testing.T keeps this package free of an import of testing, and
// it is the seam the suite's own tests use to observe a failure instead of
// suffering one. testing.TB cannot serve either purpose: it is sealed by an
// unexported method, so nothing outside package testing can implement it.
type TB interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
	Cleanup(func())
}

// pollInterval is short because a backend that answers at all answers in
// milliseconds. The deadline, not the interval, is what accommodates a slow one.
//
// A var rather than a const so this package's own tests can shrink it: they
// exercise the polling and the timeout path, and at 50ms they would spend most
// of their runtime asleep in the fast suite that has to stay fast.
var pollInterval = 50 * time.Millisecond

// timebox is the context every direct call in this suite uses. Reaching for
// context.Background() is deliberate — a suite that took the caller's context
// could not be run from a cleanup, where t.Context() is already cancelled.
func timebox(observe time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), observe)
}

// RunConformance asserts the lifecycle every runtime.Runtime implementation
// must share, against a real implementation and a spec its backend can run.
//
// observe bounds each individual call and the polling that waits for an
// operation to become visible, so it must cover the backend's slowest
// operation — an image pull, not just a list. The suite polls rather than
// asserting immediately because Docker's synchronous deletion is not the
// contract: Kubernetes and cloud-mediated runtimes only converge within a
// deadline.
//
// Only runtime.Runtime and its shared sentinels are covered here. Image
// resolution, exec, and filesystem behavior belong to their own suites; see
// docs/reference/runtime-backend-testing.mdx.
func RunConformance(t TB, rt runtime.Runtime, spec runtime.Spec, observe time.Duration) {
	t.Helper()

	created := conformCreate(t, rt, spec, observe)
	conformInspect(t, rt, created, observe)
	conformList(t, rt, created, observe)
	conformDestroy(t, rt, created.ID, observe)
	conformMalformedID(t, rt, observe)
}

func conformCreate(t TB, rt runtime.Runtime, spec runtime.Spec, observe time.Duration) runtime.Info {
	t.Helper()

	ctx, cancel := timebox(observe)
	defer cancel()

	sandboxID := runtime.NewSandboxID()
	created, err := rt.Create(ctx, sandboxID, spec)
	if err != nil {
		t.Fatalf("Create(%+v) error = %v, want nil", spec, err)
	}
	if created.ID != sandboxID {
		t.Errorf("Create ID = %q, want the supplied %q", created.ID, sandboxID)
	}

	// Registered before the assertions below, not after them: a Fatalf here
	// ends the goroutine, so cleanup written further down would never run and
	// every failing conformance run would leak a sandbox.
	if created.ID != "" {
		t.Cleanup(func() {
			ctx, cancel := timebox(observe)
			defer cancel()
			if err := rt.Destroy(ctx, created.ID); err != nil {
				t.Errorf("cleanup Destroy(%q) error = %v, want nil", created.ID, err)
			}
		})
	}

	if created.ID == "" {
		t.Fatalf("Create returned an empty id, want a backend-neutral sandbox identity")
	}
	if created.State != runtime.StateRunning {
		t.Errorf("Create state = %q, want %q", created.State, runtime.StateRunning)
	}
	if created.CreatedAt.IsZero() {
		t.Errorf("Create CreatedAt is the zero time, want the creation instant")
	}
	if loc := created.CreatedAt.Location(); loc != time.UTC {
		t.Errorf("Create CreatedAt location = %v, want UTC", loc)
	}

	return created
}

func conformInspect(t TB, rt runtime.Runtime, created runtime.Info, observe time.Duration) {
	t.Helper()

	inspected := converge(t,
		fmt.Sprintf("Inspect(%q) must observe the created sandbox", created.ID),
		observe,
		func(ctx context.Context) (runtime.Info, error) { return rt.Inspect(ctx, created.ID) },
		func(_ runtime.Info, err error) bool { return err == nil },
	)

	if inspected.ID != created.ID {
		t.Errorf("Inspect id = %q, want the created identity %q", inspected.ID, created.ID)
	}
	if inspected.CreatedAt.IsZero() {
		t.Errorf("Inspect CreatedAt is the zero time, want the creation instant")
	}

	// Compared against a second Inspect rather than against Create: the two
	// sources may have different precision — Docker's Create timestamp is the
	// local clock while its listing reports whole seconds — so equality across
	// them is not part of the contract. Equality across two observations is.
	ctx, cancel := timebox(observe)
	defer cancel()

	again, err := rt.Inspect(ctx, created.ID)
	if err != nil {
		t.Fatalf("repeat Inspect(%q) error = %v, want nil", created.ID, err)
	}
	if !again.CreatedAt.Equal(inspected.CreatedAt) {
		t.Errorf("repeat Inspect CreatedAt = %v, want the stable %v", again.CreatedAt, inspected.CreatedAt)
	}
}

func conformList(t TB, rt runtime.Runtime, created runtime.Info, observe time.Duration) {
	t.Helper()

	// Membership, not equality: a backend may hold sandboxes this suite did not
	// create, and asserting a length of one would make the suite fail on a busy
	// but correct environment.
	converge(t,
		fmt.Sprintf("List must contain the created sandbox %q", created.ID),
		observe,
		func(ctx context.Context) ([]runtime.Info, error) { return rt.List(ctx) },
		func(infos []runtime.Info, err error) bool {
			if err != nil {
				return false
			}
			for _, info := range infos {
				if info.ID == created.ID {
					return true
				}
			}
			return false
		},
	)
}

// conformDestroy also discharges the contract's "well-formed but unknown id"
// clauses: once the destroy has converged, id is exactly that — an identity the
// backend recognizes the shape of and no longer holds. Minting a never-issued id
// instead would force this backend-neutral suite to encode the private id format
// of the package it tests, and the only way to derive one from a real id — edit a
// character — silently becomes a malformed id under any format with a checksum or
// a fixed suffix.
func conformDestroy(t TB, rt runtime.Runtime, id string, observe time.Duration) {
	t.Helper()

	destroy := func(clause string) {
		// A fresh box per call rather than one for the whole function: the
		// convergence below may legitimately consume most of its own window on an
		// eventually-consistent backend, and a shared context would then report
		// the idempotency clause as a deadline rather than a verdict.
		ctx, cancel := timebox(observe)
		defer cancel()

		if err := rt.Destroy(ctx, id); err != nil {
			t.Errorf("%s: Destroy(%q) error = %v, want nil", clause, id, err)
		}
	}

	destroy("a created sandbox must be destroyable")

	converge(t,
		fmt.Sprintf("Inspect(%q) must report ErrNotFound after Destroy", id),
		observe,
		func(ctx context.Context) (runtime.Info, error) { return rt.Inspect(ctx, id) },
		func(_ runtime.Info, err error) bool { return errors.Is(err, runtime.ErrNotFound) },
	)

	// Cleanup is retry safe by contract, so every recovery path can destroy
	// without first checking whether the sandbox is still there.
	destroy("destroy must be idempotent")
}

func conformMalformedID(t TB, rt runtime.Runtime, observe time.Duration) {
	t.Helper()

	ctx, cancel := timebox(observe)
	defer cancel()

	// Malformed is a caller bug, not an absence, and the two must stay
	// distinguishable: a plane answering 404 for a typo hides the typo.
	const malformed = "not-a-sandbox-id"
	if _, err := rt.Inspect(ctx, malformed); !errors.Is(err, runtime.ErrInvalidSandboxID) {
		t.Errorf("Inspect(%q) error = %v, want ErrInvalidSandboxID", malformed, err)
	}
}

// converge polls observe until settled accepts its result, and fails the test
// with the clause it was proving and the last thing it actually saw. A fixed
// sleep would make a failure slow without making it informative.
func converge[T any](
	t TB,
	clause string,
	timeout time.Duration,
	observe func(context.Context) (T, error),
	settled func(T, error) bool,
) T {
	t.Helper()

	last, lastErr, timedOut := poll(timeout, observe, settled)
	if timedOut != nil {
		t.Fatalf("%s: %v; last observation %+v; last error %v", clause, timedOut, last, lastErr)
	}
	return last
}

// poll is separate from converge so the suite's own tests can assert what a
// timeout reports without having to fail to find out. It returns the last
// observation and error alongside the timeout, because a converge failure is
// only diagnosable if it can name what it kept seeing.
func poll[T any](
	timeout time.Duration,
	observe func(context.Context) (T, error),
	settled func(T, error) bool,
) (last T, lastErr error, timedOut error) {
	ctx, cancel := timebox(timeout)
	defer cancel()

	// The context is the only deadline. A separate time.Now()+timeout would be a
	// second copy of the same bound, free to drift from the one the observed
	// calls are themselves subject to.
	attempts := 0
	for {
		last, lastErr = observe(ctx)
		attempts++
		if settled(last, lastErr) {
			return last, lastErr, nil
		}

		// Waiting on the context as well as the interval, so a poll stops at its
		// deadline rather than overshooting it by up to one interval.
		select {
		case <-ctx.Done():
			return last, lastErr, fmt.Errorf(
				"not observed within %s after %d attempts", timeout, attempts)
		case <-time.After(pollInterval):
		}
	}
}
