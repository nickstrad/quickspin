package errs_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/nickstrad/quickspin/internal/errs"
)

type fooTag struct{}

type FooError = errs.Error[fooTag]

func fooE(op, message string, err error) *FooError { return errs.E[fooTag](op, message, err) }

func fooWrap(op, message string, err error) *FooError { return errs.Wrap[fooTag](op, message, err) }

type barTag struct{}

type BarError = errs.Error[barTag]

var errSentinel = errors.New("sentinel failure")

func TestErrorRenders(t *testing.T) {
	tests := []struct {
		name string
		err  *FooError
		want string
	}{
		{"op message cause", fooE("pkg.Type.Method", "doing the thing", errSentinel), "pkg.Type.Method: doing the thing: sentinel failure"},
		{"no cause", fooE("pkg.Type.Method", "doing the thing", nil), "pkg.Type.Method: doing the thing"},
		{"no message", fooE("pkg.Type.Method", "", errSentinel), "pkg.Type.Method: sentinel failure"},
		{"no op", fooE("", "doing the thing", errSentinel), "doing the thing: sentinel failure"},
		{"cause only", fooE("", "", errSentinel), "sentinel failure"},
		{"nothing", fooE("", "", nil), ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.Error(); got != tt.want {
				t.Errorf("Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestErrorPreservesSentinelThroughWraps(t *testing.T) {
	// The convention itself: a sentinel lost inside the envelope makes every
	// caller that branches on a failure kind misdiagnose it, silently.
	origin := fooE("pkg.Type.Method", "doing the thing", errSentinel)
	if !errors.Is(origin, errSentinel) {
		t.Fatal("errors.Is(E(...), errSentinel) = false, want true at the origin")
	}

	wrapped := fooWrap("caller.Type.Method", "calling the thing", origin)
	if !errors.Is(wrapped, errSentinel) {
		t.Error("errors.Is(Wrap(E(...)), errSentinel) = false, want true through one wrap")
	}

	twice := fooWrap("cmd.run", "running", wrapped)
	if !errors.Is(twice, errSentinel) {
		t.Error("errors.Is(...) = false, want true through two wraps")
	}
	if errors.Is(twice, errors.New("other")) {
		t.Error("errors.Is(..., other) = true, want false")
	}
}

func TestErrorUnwrapsToItsCause(t *testing.T) {
	if got := errors.Unwrap(fooE("op", "message", errSentinel)); got != errSentinel {
		t.Errorf("errors.Unwrap(E(...)) = %v, want errSentinel", got)
	}
	if got := errors.Unwrap(fooE("op", "message", nil)); got != nil {
		t.Errorf("errors.Unwrap(E(op, msg, nil)) = %v, want nil", got)
	}
}

func TestErrorAsExposesTheOutermostEnvelope(t *testing.T) {
	// Callers must not need this; a log site does, to read Op and Stack.
	origin := fooE("pkg.Type.Method", "doing the thing", errSentinel)
	wrapped := fooWrap("caller.Type.Method", "calling the thing", origin)

	var fe *FooError
	if !errors.As(wrapped, &fe) {
		t.Fatal("errors.As(wrapped, &*FooError) = false, want true")
	}
	if fe.Op != "caller.Type.Method" {
		t.Errorf("errors.As found Op %q, want the outermost envelope %q", fe.Op, "caller.Type.Method")
	}
}

func TestInstantiationsAreDistinctTypes(t *testing.T) {
	// What the tag type buys: one implementation, but a package's errors.As and
	// type switches still match only its own envelope.
	var be *BarError
	if errors.As(fooE("pkg.Type.Method", "doing the thing", errSentinel), &be) {
		t.Error("errors.As(*FooError, &*BarError) = true, want false: distinct instantiations")
	}
}

func TestEUsesTheStackOnlyAtTheOrigin(t *testing.T) {
	// Two stacks per error is the waste the E/Wrap split avoids; zero at the
	// origin leaves the log site nothing to emit.
	origin := fooE("pkg.Type.Method", "doing the thing", errSentinel)
	if origin.Stack == "" {
		t.Fatal("E(...).Stack is empty, want a captured stack at the origin")
	}
	if !strings.Contains(origin.Stack, "TestEUsesTheStackOnlyAtTheOrigin") {
		t.Error("E(...).Stack does not name the calling test, want the stack captured at the E call site")
	}

	if wrapped := fooWrap("caller.Type.Method", "calling the thing", origin); wrapped.Stack != "" {
		t.Error("Wrap(...).Stack is non-empty, want no second stack above the origin")
	}
}
