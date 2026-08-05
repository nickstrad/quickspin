// Package events is the append-only history of what happened to a sandbox: one
// immutable row per lifecycle transition. It is a record of facts, never
// derived state, so nothing here recomputes an event — see
// docs/plans/closed/06-reconciler-leases-events.mdx.
package events

import (
	"time"

	"github.com/nickstrad/quickspin/internal/sandbox"
)

type Event struct {
	ID        int               `json:"-" yaml:"-"`
	VersionID int               `json:"version_id" yaml:"version_id"`
	SandboxID string            `json:"sandbox_id" yaml:"sandbox_id"`
	FromState sandbox.TaskState `json:"from_state" yaml:"from_state"`
	ToState   sandbox.TaskState `json:"to_state" yaml:"to_state"`
	At        time.Time         `json:"at" yaml:"at"`
	Reason    string            `json:"reason" yaml:"reason"`
}

// Validate checks presence only: legality is sandbox.CanTransition's call,
// already made before the transition happened.
func (e *Event) Validate() error {
	const op = "events.Event.Validate"

	if e == nil {
		return E(op, "event is nil", ErrInvalidEvent)
	}
	if e.SandboxID == "" {
		return E(op, "event has no sandbox id", ErrInvalidEvent)
	}
	if e.VersionID < 1 {
		return E(op, "event has no version id", ErrInvalidEvent)
	}
	if e.ToState == "" {
		return E(op, "event has no to state", ErrInvalidEvent)
	}
	if e.Reason == "" {
		return E(op, "event has no reason", ErrInvalidEvent)
	}
	return nil
}
