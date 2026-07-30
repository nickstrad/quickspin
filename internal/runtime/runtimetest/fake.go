// Package runtimetest provides a runtime.Runtime implementation for testing
// code that consumes the interface. It follows the net/http/httptest pattern:
// a separate package so the test double never reaches the production API of
// internal/runtime, and non-test files so any package can import it.
package runtimetest

import (
	"context"
	"io/fs"

	"github.com/nickstrad/quickspin/internal/runtime"
)

// Fake answers each call from the matching field, so a test states the answer
// it needs — including an error — with no daemon involved.
//
// The embedded interface is nil on purpose. It satisfies runtime.Runtime
// without stub methods, so a test that only needs Inspect sets only Inspect,
// and a consumer that unexpectedly calls Create panics instead of receiving a
// zero Info. The tradeoff: adding a method to runtime.Runtime no longer breaks
// this file at compile time, it breaks callers at run time.
type Fake struct {
	runtime.Runtime

	CreateFn     func(context.Context, runtime.Spec) (runtime.Info, error)
	InspectFn    func(context.Context, string) (runtime.Info, error)
	ListFn       func(context.Context) ([]runtime.Info, error)
	DestroyFn    func(context.Context, string) error
	ExecFn       func(context.Context, string, []string, runtime.ExecOpts) (runtime.ExecResult, error)
	WriteFileFn  func(context.Context, string, string, []byte, fs.FileMode) error
	ReadFileFn   func(context.Context, string, string) ([]byte, error)
	ListDirFn    func(context.Context, string, string) ([]runtime.FileInfo, error)
	RemovePathFn func(context.Context, string, string) error
}

var _ runtime.Runtime = Fake{}

func (f Fake) Create(ctx context.Context, spec runtime.Spec) (runtime.Info, error) {
	return f.CreateFn(ctx, spec)
}

func (f Fake) Inspect(ctx context.Context, id string) (runtime.Info, error) {
	return f.InspectFn(ctx, id)
}

func (f Fake) List(ctx context.Context) ([]runtime.Info, error) {
	return f.ListFn(ctx)
}

func (f Fake) Destroy(ctx context.Context, id string) error {
	return f.DestroyFn(ctx, id)
}

func (f Fake) Exec(ctx context.Context, id string, cmd []string, opts runtime.ExecOpts) (runtime.ExecResult, error) {
	return f.ExecFn(ctx, id, cmd, opts)
}

func (f Fake) WriteFile(ctx context.Context, id, path string, content []byte, mode fs.FileMode) error {
	return f.WriteFileFn(ctx, id, path, content, mode)
}

func (f Fake) ReadFile(ctx context.Context, id, path string) ([]byte, error) {
	return f.ReadFileFn(ctx, id, path)
}

func (f Fake) ListDir(ctx context.Context, id, path string) ([]runtime.FileInfo, error) {
	return f.ListDirFn(ctx, id, path)
}

func (f Fake) RemovePath(ctx context.Context, id, path string) error {
	return f.RemovePathFn(ctx, id, path)
}
