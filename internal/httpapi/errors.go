package httpapi

import (
	"errors"

	"github.com/nickstrad/quickspin/internal/errs"
)

// Failures the handler itself originates, before any call to the store or the
// runtime: a body that will not decode, a missing path parameter. Failures from
// below arrive carrying their own package's sentinels.
var (
	ErrInvalidRequest = errors.New("invalid request")
	ErrNotFound       = errors.New("not found")
)

type apiTag struct{}

// APIError carries Op ("httpapi.API.CreateSandbox" — package.Type.Method),
// Message, Err and Stack.
type APIError = errs.Error[apiTag]

// E builds an error at an origin and captures a stack. Wrap adds an operation
// above an error that already carries one, so the stack is captured once.
func E(op, message string, err error) *APIError {
	return errs.E[apiTag](op, message, err)
}

func Wrap(op, message string, err error) *APIError {
	return errs.Wrap[apiTag](op, message, err)
}

// OpOf reports the outermost Op in the chain so a log line can name the
// operation that failed without the handler restating it.
func OpOf(err error) string {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Op
	}
	return ""
}
