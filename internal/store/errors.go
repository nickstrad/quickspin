package store

import (
	"errors"

	"github.com/nickstrad/quickspin/internal/errs"
)

// The public contract of this package: callers test these with errors.Is and
// must never parse error strings or reach into StoreError's fields. A lookup
// miss returns a bare sentinel; the database/sql boundary wraps with E. See
// docs/reference/error-handling-and-logging.mdx.
var (
	ErrNotFound      = errors.New("sandbox not found")
	ErrMissingExpiry = errors.New("sandbox expiry is missing")
)

type storeTag struct{}

// StoreError carries Op ("store.SqlliteStore.CreateSandbox" —
// package.Type.Method), Message, Err and Stack.
type StoreError = errs.Error[storeTag]

// E builds an error at an origin and captures a stack. Wrap adds an operation
// above an error that already carries one, so the stack is captured once.
func E(op, message string, err error) *StoreError {
	return errs.E[storeTag](op, message, err)
}

func Wrap(op, message string, err error) *StoreError {
	return errs.Wrap[storeTag](op, message, err)
}
