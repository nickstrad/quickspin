package client_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/nickstrad/quickspin/internal/api"
	"github.com/nickstrad/quickspin/internal/client"
	"github.com/nickstrad/quickspin/internal/httpapi"
	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/nickstrad/quickspin/internal/runtime/runtimetest"
	"github.com/nickstrad/quickspin/internal/sandbox"
	"github.com/nickstrad/quickspin/internal/store"
	"github.com/nickstrad/quickspin/internal/store/sqlite"
)

// The tests here run against the real API over a real socket, because the thing
// under test is the wire form: a client tested against a hand-written stub
// would agree with itself about a shape the server never sends.
func newTestClient(t *testing.T, rt runtime.Runtime) (*client.Client, store.Store) {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := sqlite.New(t.Context(), ":memory:", "", logger)
	if err != nil {
		t.Fatalf("sqlite.New(:memory:) error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := st.Cleanup(); err != nil {
			t.Errorf("Cleanup() error = %v, want nil", err)
		}
	})

	srv := httpapi.NewAPI("127.0.0.1", 0, logger, st, rt)
	server := httptest.NewServer(srv.Handler())
	t.Cleanup(server.Close)

	return client.New(server.URL, server.Client()), st
}

// runningSandbox creates one through the API and then transitions it the way
// the reconciler will: create only records intent, and the file and exec routes
// gate on running.
func runningSandbox(t *testing.T, c *client.Client, st store.Store) string {
	t.Helper()

	image := "alpine:3.20"
	sbx, err := c.CreateSandbox(t.Context(), "key-1", sandbox.SpecFile{Image: &image}, 0)
	if err != nil {
		t.Fatalf("CreateSandbox error = %v, want nil", err)
	}
	if sbx.State != sandbox.Pending {
		t.Fatalf("CreateSandbox state = %q, want %q", sbx.State, sandbox.Pending)
	}

	if _, err := st.UpdateSandboxState(t.Context(), sbx.SandboxID, sandbox.Pending, sandbox.Running, "test"); err != nil {
		t.Fatalf("UpdateSandboxState(pending, running) error = %v, want nil", err)
	}
	return sbx.SandboxID
}

// No CreateFn: no route starts a container, so a server that tries panics here.
func okRuntime() runtimetest.Fake {
	return runtimetest.Fake{}
}

func TestCreateAndListRoundTrip(t *testing.T) {
	c, st := newTestClient(t, okRuntime())
	id := runningSandbox(t, c, st)

	sbxs, err := c.ListSandboxes(t.Context())
	if err != nil {
		t.Fatalf("ListSandboxes error = %v, want nil", err)
	}
	if len(sbxs) != 1 || sbxs[0].SandboxID != id {
		t.Fatalf("ListSandboxes = %+v, want the one sandbox just created", sbxs)
	}
	// The spec has to survive the round trip through the store's JSON column,
	// since it is what a later reconcile would rebuild the sandbox from.
	if sbxs[0].Spec.Image == nil || *sbxs[0].Spec.Image != "alpine:3.20" {
		t.Errorf("Spec.Image = %v, want alpine:3.20", sbxs[0].Spec.Image)
	}
}

func TestRepeatedIdempotencyKeyReturnsTheSameSandbox(t *testing.T) {
	c, _ := newTestClient(t, okRuntime())

	image := "alpine:3.20"
	first, err := c.CreateSandbox(t.Context(), "key-1", sandbox.SpecFile{Image: &image}, 0)
	if err != nil {
		t.Fatalf("first CreateSandbox error = %v, want nil", err)
	}
	second, err := c.CreateSandbox(t.Context(), "key-1", sandbox.SpecFile{Image: &image}, 0)
	if err != nil {
		t.Fatalf("second CreateSandbox error = %v, want nil", err)
	}

	if first.SandboxID != second.SandboxID {
		t.Errorf("sandbox ids = %q and %q, want one key to yield one sandbox",
			first.SandboxID, second.SandboxID)
	}
}

func TestExecResultCrossesTheWireIntact(t *testing.T) {
	// Every field matters and none is the zero value: the exit code and the
	// truncation flags are exactly what a caller cannot re-derive from the bytes.
	want := runtime.ExecResult{
		ExitCode:        137,
		Stdout:          []byte{0x00, 0xff, 'o', 'k'},
		Stderr:          []byte("boom\n"),
		StdoutTruncated: true,
		StderrTruncated: true,
	}
	rt := okRuntime()
	rt.ExecFn = func(context.Context, string, []string, runtime.ExecOpts) (runtime.ExecResult, error) {
		return want, nil
	}

	c, st := newTestClient(t, rt)
	got, err := c.Exec(t.Context(), runningSandbox(t, c, st), []string{"sh", "-c", "exit 137"}, runtime.ExecOpts{})
	if err != nil {
		t.Fatalf("Exec error = %v, want nil", err)
	}

	if got.ExitCode != want.ExitCode ||
		!bytes.Equal(got.Stdout, want.Stdout) || !bytes.Equal(got.Stderr, want.Stderr) ||
		got.StdoutTruncated != want.StdoutTruncated || got.StderrTruncated != want.StderrTruncated {
		t.Errorf("Exec result = %+v, want %+v", got, want)
	}
}

func TestFileRoutesRoundTrip(t *testing.T) {
	var (
		gotPath    string
		gotContent []byte
		gotMode    fs.FileMode
		removed    string
	)
	rt := okRuntime()
	rt.WriteFileFn = func(_ context.Context, _, path string, content []byte, mode fs.FileMode) error {
		gotPath, gotContent, gotMode = path, bytes.Clone(content), mode
		return nil
	}
	rt.ReadFileFn = func(context.Context, string, string) ([]byte, error) {
		return gotContent, nil
	}
	rt.ListDirFn = func(_ context.Context, _, path string) ([]runtime.FileInfo, error) {
		return []runtime.FileInfo{{Path: path, Size: 4, Mode: fs.ModeDir | 0o750, IsDir: true}}, nil
	}
	rt.RemovePathFn = func(_ context.Context, _, path string) error {
		removed = path
		return nil
	}

	c, st := newTestClient(t, rt)
	id := runningSandbox(t, c, st)
	ctx := t.Context()

	// Bytes that are not valid UTF-8, since base64 is what the envelope relies
	// on to carry them unchanged.
	content := []byte{0x00, 0xff, 0x10, '\n'}
	if err := c.WriteFile(ctx, id, "/work/a.bin", content, 0o640); err != nil {
		t.Fatalf("WriteFile error = %v, want nil", err)
	}
	if gotPath != "/work/a.bin" || !bytes.Equal(gotContent, content) || gotMode != 0o640 {
		t.Errorf("WriteFile reached the runtime as %q %v %#o, want /work/a.bin %v %#o",
			gotPath, gotContent, gotMode, content, fs.FileMode(0o640))
	}

	read, err := c.ReadFile(ctx, id, "/work/a.bin")
	if err != nil {
		t.Fatalf("ReadFile error = %v, want nil", err)
	}
	if !bytes.Equal(read, content) {
		t.Errorf("ReadFile = %v, want %v", read, content)
	}

	infos, err := c.ListDir(ctx, id, "/work")
	if err != nil {
		t.Fatalf("ListDir error = %v, want nil", err)
	}
	if len(infos) != 1 || infos[0].Path != "/work" || !infos[0].IsDir || infos[0].Mode != fs.ModeDir|0o750 {
		t.Errorf("ListDir = %+v, want the directory entry with its mode intact", infos)
	}

	if err := c.RemovePath(ctx, id, "/work/a.bin"); err != nil {
		t.Fatalf("RemovePath error = %v, want nil", err)
	}
	if removed != "/work/a.bin" {
		t.Errorf("RemovePath reached the runtime as %q, want /work/a.bin", removed)
	}
}

func TestDestroyIsIdempotent(t *testing.T) {
	rt := okRuntime()
	rt.DestroyFn = func(context.Context, string) error { return nil }

	c, st := newTestClient(t, rt)
	id := runningSandbox(t, c, st)

	if err := c.DestroySandbox(t.Context(), id); err != nil {
		t.Fatalf("first DestroySandbox error = %v, want nil", err)
	}
	// The sandbox is already gone, which is the outcome the caller asked for.
	if err := c.DestroySandbox(t.Context(), id); err != nil {
		t.Errorf("second DestroySandbox error = %v, want nil", err)
	}
}

func TestErrorEnvelopeBecomesATypedError(t *testing.T) {
	c, _ := newTestClient(t, okRuntime())

	_, err := c.InspectSandbox(t.Context(), "sbx_does-not-exist")
	if !client.HasCode(err, api.CodeNotFound) {
		t.Fatalf("InspectSandbox error = %v, want the not_found code", err)
	}

	var apiErr *client.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("InspectSandbox error = %v, want a *client.Error", err)
	}
	if apiErr.Status != http.StatusNotFound {
		t.Errorf("Status = %d, want %d", apiErr.Status, http.StatusNotFound)
	}
	// Prose, not the code: the message is free to be reworded, which is exactly
	// why it must not be the thing a caller branches on.
	if apiErr.Message == "" {
		t.Error("Message = \"\", want the server's prose alongside the code")
	}
}
