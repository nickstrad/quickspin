package runtime

import (
	"errors"
	"strings"
	"testing"
)

func TestRuntimeErrorPreservesSentinel(t *testing.T) {
	// The convention itself: a sentinel lost inside the envelope makes every
	// caller that branches on a failure kind misdiagnose it, silently.
	origin := E("runtime.dockerRuntime.Inspect", "inspecting container for sandbox sbx_a1b2c3", ErrNotFound)

	if !errors.Is(origin, ErrNotFound) {
		t.Fatal("errors.Is(E(...), ErrNotFound) = false, want true at the origin")
	}

	wrapped := Wrap("controlplane.Service.GetSandbox", "reading sandbox sbx_a1b2c3", origin)
	if !errors.Is(wrapped, ErrNotFound) {
		t.Error("errors.Is(Wrap(E(...)), ErrNotFound) = false, want true through one wrap")
	}

	twice := Wrap("cmd.sandboxInspect", "running inspect", wrapped)
	if !errors.Is(twice, ErrNotFound) {
		t.Error("errors.Is(...) = false, want true through two wraps")
	}
	if errors.Is(twice, ErrImageMissing) {
		t.Error("errors.Is(..., ErrImageMissing) = true, want false: the chain carries only ErrNotFound")
	}
}

func TestRuntimeErrorAsExposesTheEnvelope(t *testing.T) {
	// Callers must not need this; a log site does, to read Op and Stack.
	origin := E("runtime.dockerRuntime.Create", "pulling image alpine:3.20", ErrImageMissing)
	wrapped := Wrap("controlplane.Service.CreateSandbox", "creating sandbox", origin)

	var re *RuntimeError
	if !errors.As(wrapped, &re) {
		t.Fatal("errors.As(wrapped, &*RuntimeError) = false, want true")
	}
	if re.Op != "controlplane.Service.CreateSandbox" {
		t.Errorf("errors.As found Op %q, want the outermost envelope %q", re.Op, "controlplane.Service.CreateSandbox")
	}
}

func TestRuntimeErrorRendersOpMessageCause(t *testing.T) {
	err := E("runtime.dockerRuntime.Inspect", "inspecting container for sandbox sbx_a1b2c3", ErrNotFound)

	want := "runtime.dockerRuntime.Inspect: inspecting container for sandbox sbx_a1b2c3: sandbox not found"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestRuntimeErrorRendersWithoutACause(t *testing.T) {
	// No cause must not render a dangling separator.
	err := E("runtime.dockerRuntime.Create", "generating sandbox id", nil)

	want := "runtime.dockerRuntime.Create: generating sandbox id"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestEUsesTheStackOnlyAtTheOrigin(t *testing.T) {
	// Two stacks per error is the waste the E/Wrap split avoids; zero at the
	// origin leaves the log site nothing to emit.
	origin := E("runtime.dockerRuntime.Create", "starting container", ErrImageMissing)
	if origin.Stack == "" {
		t.Error("E(...).Stack is empty, want a captured stack at the origin")
	}
	if !strings.Contains(origin.Stack, "TestEUsesTheStackOnlyAtTheOrigin") {
		t.Error("E(...).Stack does not name the calling test, want the stack captured at the E call site")
	}

	if wrapped := Wrap("cmd.sandboxCreate", "running create", origin); wrapped.Stack != "" {
		t.Error("Wrap(...).Stack is non-empty, want no second stack above the origin")
	}
}

func TestRuntimeErrorUnwrapsToItsCause(t *testing.T) {
	origin := E("runtime.dockerRuntime.Inspect", "inspecting container", ErrNotFound)

	if got := errors.Unwrap(origin); got != ErrNotFound {
		t.Errorf("errors.Unwrap(E(...)) = %v, want ErrNotFound", got)
	}
	if got := errors.Unwrap(E("op", "message", nil)); got != nil {
		t.Errorf("errors.Unwrap(E(op, msg, nil)) = %v, want nil", got)
	}
}
