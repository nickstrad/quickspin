package runtime

import (
	"archive/tar"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"testing"
)

// Entry selection, recursion, path joining, and the entry cap are pinned in
// file_archive_test.go against listDirectoryFromTarStream directly; these tests
// cover what the adapter adds — which path the daemon is asked for, and which
// failures reach the caller as which sentinel.
func TestListDirAsksForTheDirectoryPathAndReturnsItsEntries(t *testing.T) {
	const dirPath = "/work"

	daemon := newFakeDaemon(t)
	daemon.list = listOKManaged()
	daemon.copyFrom = copyFromOK(t, tarEntries(t,
		testTarEntry{tar.Header{Name: "work/", Typeflag: tar.TypeDir, Mode: 0o755}, nil},
		testTarEntry{tar.Header{Name: "work/main.go", Typeflag: tar.TypeReg, Mode: 0o640, Size: 13}, []byte("package main\n")},
		testTarEntry{tar.Header{Name: "work/logs/", Typeflag: tar.TypeDir, Mode: 0o750}, nil},
		testTarEntry{tar.Header{Name: "work/logs/app.log", Typeflag: tar.TypeReg, Mode: 0o600, Size: 8}, []byte("started\n")},
	))
	rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

	got, err := rt.ListDir(t.Context(), testSandboxID, dirPath)
	if err != nil {
		t.Fatalf("ListDir error = %v, want nil", err)
	}
	assertFileInfos(t, got, []FileInfo{
		{Path: "/work/logs", Mode: fs.ModeDir | 0o750, IsDir: true},
		{Path: "/work/logs/app.log", Size: 8, Mode: 0o600},
		{Path: "/work/main.go", Size: 13, Mode: 0o640},
	})

	request := daemon.lastMatching(http.MethodGet)
	if want := "/containers/" + testContainerID + "/archive"; !strings.HasSuffix(request.path, want) {
		t.Errorf("list request path = %q, want %q", request.path, want)
	}
	// The full path goes to the daemon: a truncated or rewritten path here lists
	// a different directory rather than failing.
	if got := request.query.Get("path"); got != dirPath {
		t.Errorf("list path = %q, want %q", got, dirPath)
	}
}

func TestListDirRejectsInvalidPathBeforeDocker(t *testing.T) {
	daemon := newFakeDaemon(t)
	rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

	got, err := rt.ListDir(t.Context(), testSandboxID, "/work/../etc")
	if !errors.Is(err, ErrInvalidPath) {
		t.Fatalf("ListDir error = %v, want ErrInvalidPath", err)
	}
	if got != nil {
		t.Errorf("ListDir = %+v, want no entries alongside an error", got)
	}
	if routes := daemon.routes(); len(routes) != 0 {
		t.Errorf("requests = %v, want none for a path rejected before I/O", routes)
	}
	// The operation is in the message because that is all a user sees; a listing
	// that reports itself as a read sends them to the wrong call.
	if !strings.Contains(err.Error(), "listing") {
		t.Errorf("ListDir error = %q, want it to describe a listing", err)
	}
}

// Unlike ReadFile, "/" is a legitimate target here: it is a directory, and
// validateRead's extra rejection of it would make the sandbox root unlistable.
// Only the rejection is asserted — what the daemon returns for a root source is
// not this test's subject.
func TestListDirAcceptsTheSandboxRoot(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.list = listOKManaged()
	rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

	_, err := rt.ListDir(t.Context(), testSandboxID, "/")
	if errors.Is(err, ErrInvalidPath) {
		t.Fatalf("ListDir(%q) error = %v, want the root to be a valid target", "/", err)
	}

	request := daemon.lastMatching(http.MethodGet)
	if !strings.HasSuffix(request.path, "/archive") {
		t.Fatalf("last GET = %q, want the archive endpoint to be reached", request.path)
	}
	if got := request.query.Get("path"); got != "/" {
		t.Errorf("list path = %q, want %q", got, "/")
	}
}

// An unknown sandbox and an absent directory are different recoveries — create
// one versus fix the path — and the wrapping is what keeps both reachable by
// errors.Is at the call site.
func TestListDirKeepsTheAbsenceSentinelsDistinct(t *testing.T) {
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
			name: "the daemon reports no such directory",
			set: func(d *fakeDaemon) {
				d.list = listOKManaged()
				d.copyFrom = dockerError(http.StatusNotFound,
					"Could not find the file /work/nope in container "+testContainerID)
			},
			want: ErrPathNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			daemon := newFakeDaemon(t)
			tt.set(daemon)
			rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

			got, err := rt.ListDir(t.Context(), testSandboxID, "/work/nope")
			if !errors.Is(err, tt.want) {
				t.Fatalf("ListDir error = %v, want errors.Is(..., %v)", err, tt.want)
			}
			if got != nil {
				t.Errorf("ListDir = %+v, want no entries alongside an error", got)
			}
		})
	}
}

func TestListDirRetainsDockerCopyFailure(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.list = listOKManaged()
	daemon.copyFrom = dockerError(http.StatusInternalServerError, "error reading from container")
	rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

	_, err := rt.ListDir(t.Context(), testSandboxID, "/work")
	if err == nil || !strings.Contains(err.Error(), "error reading from container") {
		t.Fatalf("ListDir error = %v, want Docker's copy failure", err)
	}
}
