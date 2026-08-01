package runtime

import (
	"errors"
	"runtime/debug"
	"strings"
)

// The public contract of this package: callers test these with errors.Is and
// must never parse error strings or reach into RuntimeError's fields. Pure
// helpers return a bare sentinel; the Docker SDK boundary wraps with E. See
// docs/reference/error-handling-and-logging.mdx.
var (
	ErrNotFound         = errors.New("sandbox not found")
	ErrImageMissing     = errors.New("image not found")
	ErrInvalidSandboxID = errors.New("invalid sandbox id")
	ErrInvalidSpec      = errors.New("invalid sandbox spec")
)

type RuntimeError struct {
	Op      string // "docker.Runtime.Create" — package.Type.Method
	Message string
	Err     error
	Stack   string
}

// E builds an error at an origin and captures a stack. Wrap adds an operation
// above an error that already carries one, so the stack is captured once.
func E(op, message string, err error) *RuntimeError {
	return &RuntimeError{
		Op:      op,
		Message: message,
		Err:     err,
		Stack:   string(debug.Stack()),
	}
}

func Wrap(op, message string, err error) *RuntimeError {
	return &RuntimeError{Op: op, Message: message, Err: err}
}

func (e *RuntimeError) Error() string {
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

func (e *RuntimeError) Unwrap() error { return e.Err }
