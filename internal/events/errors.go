package events

import (
	"errors"

	"github.com/nickstrad/quickspin/internal/errs"
)

// The public contract of this package: callers test these with errors.Is and
// must never parse error strings or reach into EventError's fields. See
// docs/reference/error-handling-and-logging.mdx.
var (
	ErrInvalidEvent = errors.New("invalid event")
)

type eventTag struct{}

// EventError carries Op ("events.Event.Validate" — package.Type.Method),
// Message, Err and Stack.
type EventError = errs.Error[eventTag]

// E builds an error at an origin and captures a stack. Wrap adds an operation
// above an error that already carries one, so the stack is captured once.
func E(op, message string, err error) *EventError {
	return errs.E[eventTag](op, message, err)
}

func Wrap(op, message string, err error) *EventError {
	return errs.Wrap[eventTag](op, message, err)
}
