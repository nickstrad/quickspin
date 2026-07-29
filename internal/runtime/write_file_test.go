package runtime

import (
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// Entry-level archive properties (parent dirs, modes, content) are pinned in
// file_archive_test.go; this test covers only what the adapter adds — the
// destination, the copy options, and that the archive fileArchive built is the
// one sent.
func TestWriteFileSendsFileArchiveToContainerRoot(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.list = listOKManaged()
	rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

	content := []byte{0x00, 0xff, 'q', 'u', 'i', 'c', 'k', '\n'}
	const mode = 0o640
	const filePath = "/work/a/b/main.bin"
	if err := rt.WriteFile(t.Context(), testSandboxID, filePath, content, mode); err != nil {
		t.Fatalf("WriteFile error = %v, want nil", err)
	}

	request := daemon.lastMatching(http.MethodPut)
	if got := request.query.Get("path"); got != "/" {
		t.Errorf("copy destination = %q, want root so the archive can create missing parents", got)
	}
	if got := request.query.Get("copyUIDGID"); got != "true" {
		t.Errorf("copyUIDGID = %q, want %q", got, "true")
	}
	if request.query.Has("noOverwriteDirNonDir") {
		t.Error("noOverwriteDirNonDir is set; overwriting a directory with a file should stay allowed")
	}

	want, err := fileArchive(filePath, content, mode)
	if err != nil {
		t.Fatalf("fileArchive error = %v, want nil", err)
	}
	if !bytes.Equal(request.body, want) {
		t.Error("copy body is not fileArchive's output")
	}
}

func TestWriteFileRejectsInvalidPathBeforeDocker(t *testing.T) {
	daemon := newFakeDaemon(t)
	rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

	err := rt.WriteFile(t.Context(), testSandboxID, "/work/../etc/passwd", []byte("nope"), 0o600)
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("WriteFile error = %v, want ErrInvalidPath", err)
	}
	if got := daemon.routes(); len(got) != 0 {
		t.Errorf("requests = %v, want none for a path rejected before I/O", got)
	}
}

func TestWriteFileRetainsDockerCopyFailure(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.list = listOKManaged()
	daemon.copyTo = dockerError(http.StatusInternalServerError, "archive extraction failed")
	rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

	err := rt.WriteFile(t.Context(), testSandboxID, "/work/main.go", []byte("package main"), 0o644)
	if err == nil || !strings.Contains(err.Error(), "archive extraction failed") {
		t.Fatalf("WriteFile error = %v, want Docker's copy failure", err)
	}
}

// Pins the ErrNotFound sentinel through WriteFile's wrapping so the error
// plumbing can be restructured without silently breaking errors.Is callers.
func TestWriteFilePreservesNotFoundSentinel(t *testing.T) {
	daemon := newFakeDaemon(t)
	rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

	err := rt.WriteFile(t.Context(), testSandboxID, "/work/main.go", []byte("package main"), 0o644)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("WriteFile error = %v, want ErrNotFound", err)
	}
}
