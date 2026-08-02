package sandbox

import (
	"errors"

	"github.com/nickstrad/quickspin/internal/errs"
)

// The public contract of this package: callers test these with errors.Is and
// must never parse error strings or reach into SandboxError's fields. The pure
// transition check returns a bare sentinel; spec resolution wraps with E. See
// docs/reference/error-handling-and-logging.mdx.
var (
	ErrInvalidState           = errors.New("invalid state")
	ErrInvalidStateTransition = errors.New("invalid state transition")
	ErrInvalidSpec            = errors.New("invalid sandbox spec")
	ErrSandboxNotRunning      = errors.New("sandbox not in running state")
)

type sandboxTag struct{}

// SandboxError carries Op ("sandbox.SpecFile.Resolve" — package.Type.Method),
// Message, Err and Stack.
type SandboxError = errs.Error[sandboxTag]

// E builds an error at an origin and captures a stack. Wrap adds an operation
// above an error that already carries one, so the stack is captured once.
func E(op, message string, err error) *SandboxError {
	return errs.E[sandboxTag](op, message, err)
}

func Wrap(op, message string, err error) *SandboxError {
	return errs.Wrap[sandboxTag](op, message, err)
}
