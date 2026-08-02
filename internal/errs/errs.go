// Package errs holds the one error envelope every internal package shares.
// A package declares an unexported tag type and aliases Error[tag], which keeps
// its error a distinct type for errors.As and type switches while the struct,
// the E/Wrap split, and the rendering live here once. See
// docs/reference/error-handling-and-logging.mdx.
//
// It must not import anything from internal/: every package that owns an
// envelope imports it.
package errs

import (
	"runtime/debug"
	"strings"
)

// T is a phantom type parameter — it names the owning package and appears in no
// field, so Error[storeTag] and Error[runtimeTag] are unrelated types.
type Error[T any] struct {
	Op      string // package.Type.Method
	Message string
	Err     error
	Stack   string
}

// E builds an error at an origin and captures a stack. Wrap adds an operation
// above an error that already carries one, so the stack is captured once.
//
// The captured stack begins one frame lower than it used to: E itself and the
// owning package's thin wrapper both appear above the origin call site.
func E[T any](op, message string, err error) *Error[T] {
	return &Error[T]{
		Op:      op,
		Message: message,
		Err:     err,
		Stack:   string(debug.Stack()),
	}
}

func Wrap[T any](op, message string, err error) *Error[T] {
	return &Error[T]{Op: op, Message: message, Err: err}
}

func (e *Error[T]) Error() string {
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

func (e *Error[T]) Unwrap() error { return e.Err }
