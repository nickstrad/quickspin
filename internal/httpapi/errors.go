package httpapi

import (
	"errors"
	"net/http"
	"runtime/debug"
	"strings"
)

// Failures the handler itself originates, before any call to the store or the
// runtime: a body that will not decode, a missing path parameter. Failures from
// below arrive carrying their own package's sentinels.
var (
	ErrInvalidRequest = errors.New("invalid request")
	ErrNotFound       = errors.New("not found")
)

type APIError struct {
	Op      string // "httpapi.API.CreateSandbox" — package.Type.Method
	Message string
	Err     error
	Stack   string
}

// E builds an error at an origin and captures a stack. Wrap adds an operation
// above an error that already carries one, so the stack is captured once.
func E(op, message string, err error) *APIError {
	return &APIError{
		Op:      op,
		Message: message,
		Err:     err,
		Stack:   string(debug.Stack()),
	}
}

func Wrap(op, message string, err error) *APIError {
	return &APIError{Op: op, Message: message, Err: err}
}

func (e *APIError) Error() string {
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

func (e *APIError) Unwrap() error { return e.Err }

// OpOf reports the outermost Op in the chain so a log line can name the
// operation that failed without the handler restating it.
func OpOf(err error) string {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Op
	}
	return ""
}

// StatusOf maps this package's own sentinels to a status. Sentinels from below
// (store.ErrNotFound, runtime.ErrImageMissing) stay unknown here on purpose:
// httpapi is imported by the SDK clients, and importing store or runtime to
// classify them would drag the server's dependencies into every client.
func StatusOf(err error) (int, bool) {
	switch {
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound, true
	case errors.Is(err, ErrInvalidRequest):
		return http.StatusBadRequest, true
	default:
		return http.StatusInternalServerError, false
	}
}
