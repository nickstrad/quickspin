package events

import (
	"errors"
	"strings"
	"testing"
)

func TestEventErrorPreservesSentinel(t *testing.T) {
	// The convention itself: a sentinel lost inside the envelope makes every
	// caller that branches on a failure kind misdiagnose it, silently.
	origin := E("events.Event.Validate", "event has no reason", ErrInvalidEvent)

	if !errors.Is(origin, ErrInvalidEvent) {
		t.Fatal("errors.Is(E(...), ErrInvalidEvent) = false, want true at the origin")
	}

	wrapped := Wrap("sqlite.Store.appendEvent", "recording sbx_a1b2c3 pending -> running", origin)
	if !errors.Is(wrapped, ErrInvalidEvent) {
		t.Error("errors.Is(Wrap(E(...)), ErrInvalidEvent) = false, want true through one wrap")
	}

	twice := Wrap("control.Control.DestroySandbox", "destroying the sandbox", wrapped)
	if !errors.Is(twice, ErrInvalidEvent) {
		t.Error("errors.Is(...) = false, want true through two wraps")
	}
}

func TestEventErrorAsExposesTheEnvelope(t *testing.T) {
	// Callers must not need this; a log site does, to read Op and Stack.
	origin := E("events.Event.Validate", "event has no sandbox id", ErrInvalidEvent)
	wrapped := Wrap("sqlite.Store.appendEvent", "recording the transition", origin)

	var ee *EventError
	if !errors.As(wrapped, &ee) {
		t.Fatal("errors.As(wrapped, &*EventError) = false, want true")
	}
	if ee.Op != "sqlite.Store.appendEvent" {
		t.Errorf("errors.As found Op %q, want the outermost envelope %q", ee.Op, "sqlite.Store.appendEvent")
	}
}

func TestEventErrorRendersOpMessageCause(t *testing.T) {
	err := E("events.Event.Validate", "event has no reason", ErrInvalidEvent)

	want := "events.Event.Validate: event has no reason: invalid event"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestEventErrorRendersWithoutACause(t *testing.T) {
	// No cause must not render a dangling separator.
	err := E("events.Event.Validate", "event is nil", nil)

	want := "events.Event.Validate: event is nil"
	if got := err.Error(); got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestEUsesTheStackOnlyAtTheOrigin(t *testing.T) {
	// Two stacks per error is the waste the E/Wrap split avoids; zero at the
	// origin leaves the log site nothing to emit.
	origin := E("events.Event.Validate", "event has no reason", ErrInvalidEvent)
	if origin.Stack == "" {
		t.Error("E(...).Stack is empty, want a captured stack at the origin")
	}
	if !strings.Contains(origin.Stack, "TestEUsesTheStackOnlyAtTheOrigin") {
		t.Error("E(...).Stack does not name the calling test, want the stack captured at the E call site")
	}

	if wrapped := Wrap("sqlite.Store.appendEvent", "recording the transition", origin); wrapped.Stack != "" {
		t.Error("Wrap(...).Stack is non-empty, want no second stack above the origin")
	}
}

func TestEventErrorUnwrapsToItsCause(t *testing.T) {
	origin := E("events.Event.Validate", "event has no reason", ErrInvalidEvent)

	if got := errors.Unwrap(origin); got != ErrInvalidEvent {
		t.Errorf("errors.Unwrap(E(...)) = %v, want ErrInvalidEvent", got)
	}
	if got := errors.Unwrap(E("op", "message", nil)); got != nil {
		t.Errorf("errors.Unwrap(E(op, msg, nil)) = %v, want nil", got)
	}
}
