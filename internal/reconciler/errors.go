package reconciler

import (
	"errors"

	"github.com/nickstrad/quickspin/internal/errs"
)

// ErrPassInFlight is returned when a pass is skipped because one is already
// running; callers log it as a skipped tick, not a failure.
var ErrPassInFlight = errors.New("reconcile pass already in flight")

type reconcilerTag struct{}

// ReconcilerError carries Op ("reconciler.Reconciler.ReconcileOnce" —
// package.Type.Method), Message, Err and Stack.
type ReconcilerError = errs.Error[reconcilerTag]

// E builds an error at an origin and captures a stack. Wrap adds an operation
// above an error that already carries one, so the stack is captured once.
func E(op, message string, err error) *ReconcilerError {
	return errs.E[reconcilerTag](op, message, err)
}

func Wrap(op, message string, err error) *ReconcilerError {
	return errs.Wrap[reconcilerTag](op, message, err)
}
