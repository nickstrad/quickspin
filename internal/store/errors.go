package store

import (
	"errors"
	"runtime/debug"
	"strings"
)

// The public contract of this package: callers test these with errors.Is and
// must never parse error strings or reach into StoreError's fields. The pure
// transition check returns a bare sentinel; the database/sql boundary wraps
// with E. See docs/reference/error-handling-and-logging.mdx.
var (
	ErrNotFound               = errors.New("sandbox not found")
	ErrInvalidState           = errors.New("invalid state")
	ErrInvalidStateTransition = errors.New("invalid state transition")
	ErrInvalidSpec            = errors.New("invalid sandbox spec")
	ErrSandboxNotRunning      = errors.New("sandbox not in running state")
)

type StoreError struct {
	Op      string // "store.SqlliteStore.CreateSandbox" — package.Type.Method
	Message string
	Err     error
	Stack   string
}

// E builds an error at an origin and captures a stack. Wrap adds an operation
// above an error that already carries one, so the stack is captured once.
func E(op, message string, err error) *StoreError {
	return &StoreError{
		Op:      op,
		Message: message,
		Err:     err,
		Stack:   string(debug.Stack()),
	}
}

func Wrap(op, message string, err error) *StoreError {
	return &StoreError{Op: op, Message: message, Err: err}
}

func (e *StoreError) Error() string {
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

func (e *StoreError) Unwrap() error { return e.Err }
