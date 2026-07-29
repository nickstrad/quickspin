package runtime

import (
	"archive/tar"
	"bytes"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
)

// Tar decoding, the size cap, and the absent-entry verdict are pinned in
// file_archive_test.go against fileUnarchive directly; this test covers what the
// adapter adds — which path is asked for, and that the archive coming back is
// the one whose bytes are returned.
func TestReadFileAsksForTheSourcePathAndReturnsItsContent(t *testing.T) {
	content := testBinaryContent
	const filePath = "/work/a/b/main.bin"

	daemon := newFakeDaemon(t)
	daemon.list = listOKManaged()
	// The daemon answers a file source with a single entry named for the
	// basename, not for the path that was requested.
	daemon.copyFrom = copyFromOK(t, tarEntries(t,
		testTarEntry{tar.Header{Name: "main.bin", Typeflag: tar.TypeReg, Mode: 0o640, Size: int64(len(content))}, content},
	))
	rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

	got, err := rt.ReadFile(t.Context(), testSandboxID, filePath)
	if err != nil {
		t.Fatalf("ReadFile error = %v, want nil", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("ReadFile = %v, want the archived bytes %v", got, content)
	}

	request := daemon.lastMatching(http.MethodGet)
	if want := "/containers/" + testContainerID + "/archive"; !strings.HasSuffix(request.path, want) {
		t.Errorf("read request path = %q, want %q", request.path, want)
	}
	// The full path goes to the daemon: unlike the write side, nothing is
	// carried inside an archive, so a truncated or rewritten path here reads a
	// different file rather than failing.
	if got := request.query.Get("path"); got != filePath {
		t.Errorf("read path = %q, want %q", got, filePath)
	}
}

func TestReadFileRejectsInvalidPathBeforeDocker(t *testing.T) {
	daemon := newFakeDaemon(t)
	rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

	_, err := rt.ReadFile(t.Context(), testSandboxID, "/work/../etc/passwd")
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("ReadFile error = %v, want ErrInvalidPath", err)
	}
	if got := daemon.routes(); len(got) != 0 {
		t.Errorf("requests = %v, want none for a path rejected before I/O", got)
	}
	// The operation is in the message because that is all a user sees; a read
	// that reports itself as a write sends them to the wrong call.
	if !strings.Contains(err.Error(), "reading") {
		t.Errorf("ReadFile error = %q, want it to describe a read", err)
	}
}

// The cap exists so one oversized file cannot exhaust the control process, which
// only holds if the refusal happens before the bytes move. The daemon reports
// the size in the stat that accompanies the archive, so the answer is available
// without transferring anything — this fake sends the stat and no body at all,
// and an implementation that waits for the stream sees an absence rather than a
// size.
func TestReadFileRefusesAnOversizedFileBeforeTransferringIt(t *testing.T) {
	oversized := container.PathStat{Name: "core.dump", Size: MaxFileSize + 1, Mode: 0o644}

	daemon := newFakeDaemon(t)
	daemon.list = listOKManaged()
	// An explicitly empty body, not an empty archive: an implementation that
	// waits for the stream finds nothing to size, so passing this test means the
	// refusal came from the stat.
	daemon.copyFrom = copyFromStat(t, oversized, []byte{})
	rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

	got, err := rt.ReadFile(t.Context(), testSandboxID, "/work/core.dump")
	if !errors.Is(err, ErrFileTooLarge) {
		t.Fatalf("ReadFile error = %v, want ErrFileTooLarge", err)
	}
	if got != nil {
		t.Errorf("ReadFile = %d bytes, want none for a file over the cap", len(got))
	}
}

// Pins both sentinels through ReadFile's wrapping: an unknown sandbox and an
// absent path are different recoveries, and the wrapping is what makes them
// reachable by errors.Is at the call site.
func TestReadFileKeepsTheAbsenceSentinelsDistinct(t *testing.T) {
	tests := []struct {
		name string
		set  func(*fakeDaemon)
		want error
	}{
		{
			name: "no container carries the label",
			set:  func(*fakeDaemon) {},
			want: ErrNotFound,
		},
		{
			// The sandbox is there and the archive holds nothing, which is how the
			// daemon reports a directory with no such child.
			name: "the archive has no matching entry",
			set:  func(d *fakeDaemon) { d.list = listOKManaged() },
			want: ErrPathNotFound,
		},
		{
			// How a real daemon answers a path that is not there: a 404, before any
			// archive exists to be empty. The sentinel is the whole point — a caller
			// retries a transport failure and reports a missing file to the user, so
			// leaving this as a plain error makes the two indistinguishable.
			name: "the daemon reports no such file",
			set: func(d *fakeDaemon) {
				d.list = listOKManaged()
				d.copyFrom = dockerError(http.StatusNotFound,
					"Could not find the file /work/main.go in container "+testContainerID)
			},
			want: ErrPathNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			daemon := newFakeDaemon(t)
			tt.set(daemon)
			rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

			got, err := rt.ReadFile(t.Context(), testSandboxID, "/work/main.go")
			if !errors.Is(err, tt.want) {
				t.Fatalf("ReadFile error = %v, want errors.Is(..., %v)", err, tt.want)
			}
			if got != nil {
				t.Errorf("ReadFile = %v, want no content alongside an error", got)
			}
		})
	}
}

func TestReadFileRetainsDockerCopyFailure(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.list = listOKManaged()
	daemon.copyFrom = dockerError(http.StatusInternalServerError, "error reading from container")
	rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

	_, err := rt.ReadFile(t.Context(), testSandboxID, "/work/main.go")
	if err == nil || !strings.Contains(err.Error(), "error reading from container") {
		t.Fatalf("ReadFile error = %v, want Docker's copy failure", err)
	}
}
