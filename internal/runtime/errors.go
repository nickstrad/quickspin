package runtime

import (
	"errors"

	"github.com/nickstrad/quickspin/internal/errs"
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

type runtimeTag struct{}

// RuntimeError carries Op ("docker.Runtime.Create" — package.Type.Method),
// Message, Err and Stack.
type RuntimeError = errs.Error[runtimeTag]

// E builds an error at an origin and captures a stack. Wrap adds an operation
// above an error that already carries one, so the stack is captured once.
func E(op, message string, err error) *RuntimeError {
	return errs.E[runtimeTag](op, message, err)
}

func Wrap(op, message string, err error) *RuntimeError {
	return errs.Wrap[runtimeTag](op, message, err)
}
