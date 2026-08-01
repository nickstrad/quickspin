package control

import (
	"errors"
	"runtime/debug"
	"strings"
)

// ErrInternal marks a failure that is the server's, not the caller's, even
// though the chain below it carries a sentinel that would classify as a 4xx —
// an idempotency key pointing at a row that no longer exists, a transition that
// should have been legal. Transports join on it to decide status. See
// docs/reference/error-handling-and-logging.mdx.
var ErrInternal = errors.New("internal failure")

type ControlError struct {
	Op      string // "control.Control.CreateSandbox" — package.Type.Method
	Message string
	Err     error
	Stack   string
}

// E builds an error at an origin and captures a stack. Wrap adds an operation
// above an error that already carries one, so the stack is captured once.
func E(op, message string, err error) *ControlError {
	return &ControlError{
		Op:      op,
		Message: message,
		Err:     err,
		Stack:   string(debug.Stack()),
	}
}

func Wrap(op, message string, err error) *ControlError {
	return &ControlError{Op: op, Message: message, Err: err}
}

func (e *ControlError) Error() string {
	parts := make([]string, 0, 3)
	if e.Op != "" {
		parts = append(parts, e.Op)
	}
	if e.Message != "" {
		parts = append(parts, e.Message)
	}
	if e.Err != nil {
		parts = append(parts, e.Err.Error())
	}
	return strings.Join(parts, ": ")
}

func (e *ControlError) Unwrap() error { return e.Err }
