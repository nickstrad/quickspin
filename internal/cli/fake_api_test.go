package cli_test

import (
	"context"
	"io/fs"

	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/nickstrad/quickspin/internal/sandbox"
)

// Nil callbacks make unexpected API calls fail the test.
type fakeAPI struct {
	CreateFn     func(context.Context, string, sandbox.SpecFile) (*sandbox.Sandbox, error)
	ListFn       func(context.Context) ([]*sandbox.Sandbox, error)
	InspectFn    func(context.Context, string) (runtime.Info, error)
	DestroyFn    func(context.Context, string) error
	ExecFn       func(context.Context, string, []string, runtime.ExecOpts) (runtime.ExecResult, error)
	WriteFileFn  func(context.Context, string, string, []byte, fs.FileMode) error
	ReadFileFn   func(context.Context, string, string) ([]byte, error)
	ListDirFn    func(context.Context, string, string) ([]runtime.FileInfo, error)
	RemovePathFn func(context.Context, string, string) error
}

func (f fakeAPI) CreateSandbox(ctx context.Context, key string, spec sandbox.SpecFile) (*sandbox.Sandbox, error) {
	return f.CreateFn(ctx, key, spec)
}

func (f fakeAPI) ListSandboxes(ctx context.Context) ([]*sandbox.Sandbox, error) {
	return f.ListFn(ctx)
}

func (f fakeAPI) InspectSandbox(ctx context.Context, id string) (runtime.Info, error) {
	return f.InspectFn(ctx, id)
}

func (f fakeAPI) DestroySandbox(ctx context.Context, id string) error {
	return f.DestroyFn(ctx, id)
}

func (f fakeAPI) Exec(ctx context.Context, id string, cmd []string, opts runtime.ExecOpts) (runtime.ExecResult, error) {
	return f.ExecFn(ctx, id, cmd, opts)
}

func (f fakeAPI) WriteFile(ctx context.Context, id, path string, content []byte, mode fs.FileMode) error {
	return f.WriteFileFn(ctx, id, path, content, mode)
}

func (f fakeAPI) ReadFile(ctx context.Context, id, path string) ([]byte, error) {
	return f.ReadFileFn(ctx, id, path)
}

func (f fakeAPI) ListDir(ctx context.Context, id, path string) ([]runtime.FileInfo, error) {
	return f.ListDirFn(ctx, id, path)
}

func (f fakeAPI) RemovePath(ctx context.Context, id, path string) error {
	return f.RemovePathFn(ctx, id, path)
}

func sandboxRecord(id, image string, state sandbox.TaskState) *sandbox.Sandbox {
	return &sandbox.Sandbox{
		SandboxID: id,
		State:     state,
		Spec:      sandbox.SpecFile{Image: &image},
		CreatedAt: testTime,
		UpdatedAt: testTime,
	}
}
