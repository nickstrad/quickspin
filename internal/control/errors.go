package control

import (
	"errors"

	"github.com/nickstrad/quickspin/internal/errs"
)

// ErrInternal marks a failure that is the server's, not the caller's, even
// though the chain below it carries a sentinel that would classify as a 4xx —
// an idempotency key pointing at a row that no longer exists, a transition that
// should have been legal. Transports join on it to decide status. See
// docs/reference/error-handling-and-logging.mdx.
var ErrInternal = errors.New("internal failure")

type controlTag struct{}

// ControlError carries Op ("control.Control.CreateSandbox" —
// package.Type.Method), Message, Err and Stack.
type ControlError = errs.Error[controlTag]

// E builds an error at an origin and captures a stack. Wrap adds an operation
// above an error that already carries one, so the stack is captured once.
func E(op, message string, err error) *ControlError {
	return errs.E[controlTag](op, message, err)
}

func Wrap(op, message string, err error) *ControlError {
	return errs.Wrap[controlTag](op, message, err)
}
