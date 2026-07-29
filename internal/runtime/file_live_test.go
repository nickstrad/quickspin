// The live filesystem suite for plan 04. Like the other live files it is an
// external test package with no build tag, so it compiles during an ordinary
// `make test` and skips at run time.
//
// What these answer that the pure tests cannot: fileArchive and fileUnarchive
// name entries by different conventions on purpose — the write side carries the
// full path from the container root, the read side gets names relative to the
// source's parent — so the two do not compose in memory. The daemon is the
// thing that joins them, and only a live round trip proves the join is right.
package runtime_test

import (
	"bytes"
	"crypto/rand"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/nickstrad/quickspin/internal/runtime"
)

// One sandbox for the whole round-trip suite, with a distinct path per subtest.
// Creating one per case would pay the create-and-destroy cost several times to
// prove nothing extra: none of these cases is about container state.
func TestWriteThenReadRoundTrip(t *testing.T) {
	rt := liveDocker(t)
	id := newSandbox(t, rt, liveSpec(t))

	tests := []struct {
		name    string
		path    string
		content []byte
		mode    fs.FileMode
	}{
		{
			// The case CopyToContainer cannot do by itself: it extracts into a
			// destination that must already exist, so /work/a/b comes from the
			// directory entries fileArchive prepends.
			name:    "a nested path whose parents do not exist",
			path:    "/work/a/b/main.txt",
			content: []byte("package main\n"),
			mode:    0o640,
		},
		{
			name:    "a file directly at the container root",
			path:    "/roundtrip.txt",
			content: []byte("hello"),
			mode:    0o644,
		},
		{
			// Zero bytes must come back as an empty file, not as an absence: a
			// caller that cannot tell those apart retries a read that will never
			// return anything.
			name:    "an empty file",
			path:    "/work/empty.txt",
			content: nil,
			mode:    0o600,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := rt.WriteFile(t.Context(), id, tt.path, tt.content, tt.mode); err != nil {
				t.Fatalf("WriteFile(%s) error = %v, want nil", tt.path, err)
			}

			got, err := rt.ReadFile(t.Context(), id, tt.path)
			if err != nil {
				t.Fatalf("ReadFile(%s) error = %v, want nil", tt.path, err)
			}
			if !bytes.Equal(got, tt.content) {
				t.Errorf("ReadFile(%s) = %q, want %q", tt.path, got, tt.content)
			}

			// Mode is checked through the container's own stat rather than through
			// ReadFile, which returns bytes only. It is asserted because the mode
			// crosses the boundary in a tar header field that is easy to drop —
			// the content would still be perfect and the file unexecutable.
			out := strings.TrimSpace(execOrFatal(t, rt, id, sh("stat -c %a "+tt.path)))
			if want := fmt.Sprintf("%o", uint32(tt.mode)); out != want {
				t.Errorf("mode of %s = %q, want %q", tt.path, out, want)
			}
		})
	}
}

func TestBinaryContentSurvives(t *testing.T) {
	// Any string conversion anywhere in the pipeline mangles bytes that are not
	// valid UTF-8, and text-shaped test content would never notice. A few KiB of
	// random bytes is the cheapest way to catch it before any SDK exists to
	// inherit the bug.
	rt := liveDocker(t)
	id := newSandbox(t, rt, liveSpec(t))

	content := make([]byte, 8*1024)
	if _, err := rand.Read(content); err != nil {
		t.Fatalf("generate random content: %v", err)
	}

	const path = "/work/random.bin"
	if err := rt.WriteFile(t.Context(), id, path, content, 0o600); err != nil {
		t.Fatalf("WriteFile error = %v, want nil", err)
	}

	got, err := rt.ReadFile(t.Context(), id, path)
	if err != nil {
		t.Fatalf("ReadFile error = %v, want nil", err)
	}
	if !bytes.Equal(got, content) {
		// The bytes themselves are useless in a failure message; where they first
		// diverge is not.
		t.Errorf("ReadFile returned %d bytes, want %d, first difference at %d",
			len(got), len(content), firstDifference(got, content))
	}
}

func TestReadFileAbsenceAndCap(t *testing.T) {
	rt := liveDocker(t)
	id := newSandbox(t, rt, liveSpec(t))

	t.Run("a path that is not there", func(t *testing.T) {
		// The daemon answers this with a 404 rather than an empty archive, which
		// is why the sentinel has to be recovered from the SDK's error and not
		// only from an archive with no matching entry.
		if _, err := rt.ReadFile(t.Context(), id, "/work/nothing-here.txt"); !errors.Is(err, runtime.ErrPathNotFound) {
			t.Errorf("ReadFile(missing) error = %v, want ErrPathNotFound", err)
		}
	})

	t.Run("a directory is not a file", func(t *testing.T) {
		// Reading a directory returns its whole subtree as a tar. No entry is the
		// requested file, and inventing bytes from a child would be a silent wrong
		// answer.
		if err := rt.WriteFile(t.Context(), id, "/work/dir/child.txt", []byte("child"), 0o600); err != nil {
			t.Fatalf("WriteFile error = %v, want nil", err)
		}
		got, err := rt.ReadFile(t.Context(), id, "/work/dir")
		if !errors.Is(err, runtime.ErrPathNotFound) {
			t.Errorf("ReadFile(directory) error = %v, want ErrPathNotFound", err)
		}
		if got != nil {
			t.Errorf("ReadFile(directory) = %q, want no content", got)
		}
	})

	t.Run("a file over the cap", func(t *testing.T) {
		// Written by the sandbox itself: WriteFile refuses oversized content up
		// front, so the only way to have an oversized file to read is for the
		// container to produce one — which is also the realistic case, since a
		// core dump or a log is not something quickspin put there.
		const path = "/work/too-big.bin"
		size := runtime.MaxFileSize + 1
		execOrFatal(t, rt, id, sh(fmt.Sprintf("head -c %d /dev/zero > %s", size, path)))

		got, err := rt.ReadFile(t.Context(), id, path)
		if !errors.Is(err, runtime.ErrFileTooLarge) {
			t.Fatalf("ReadFile(oversized) error = %v, want ErrFileTooLarge", err)
		}
		// The cap is a memory bound, not a label: returning the content alongside
		// the error would have already spent what the refusal exists to save.
		if got != nil {
			t.Errorf("ReadFile(oversized) returned %d bytes, want none", len(got))
		}
	})
}

// The pure tests feed listDirectoryFromTarStream archives written the way the
// daemon is believed to write them. This is the test that checks the belief:
// entry naming relative to the source's parent is the assumption the whole
// path-joining rule rests on, and only the real daemon can confirm it.
func TestListDir(t *testing.T) {
	rt := liveDocker(t)
	id := newSandbox(t, rt, liveSpec(t))

	const content = "package main\n"
	writeOrFatal(t, rt, id, "/work/list/main.go", []byte(content), 0o640)
	writeOrFatal(t, rt, id, "/work/list/logs/app.log", []byte("started\n"), 0o600)

	t.Run("a directory's whole subtree", func(t *testing.T) {
		got, err := rt.ListDir(t.Context(), id, "/work/list")
		if err != nil {
			t.Fatalf("ListDir error = %v, want nil", err)
		}

		byPath := indexByPath(got)
		// The grandchild is the point: its path is joined from a deeper entry name
		// than the children's, and /work/list itself is not in its own listing. A
		// duplicated path lands here too, by collapsing the map.
		if len(byPath) != 3 {
			t.Fatalf("listing = %+v, want main.go, logs, and logs/app.log under /work/list", got)
		}
		if grandchild, ok := byPath["/work/list/logs/app.log"]; !ok {
			t.Errorf("listing = %+v, want an entry for /work/list/logs/app.log", got)
		} else if grandchild.Size != int64(len("started\n")) {
			t.Errorf("app.log size = %d, want %d", grandchild.Size, len("started\n"))
		}

		file, ok := byPath["/work/list/main.go"]
		if !ok {
			t.Fatalf("listing = %+v, want an entry for /work/list/main.go", got)
		}
		if file.IsDir {
			t.Error("main.go IsDir = true, want false")
		}
		if file.Size != int64(len(content)) {
			t.Errorf("main.go size = %d, want %d", file.Size, len(content))
		}
		// The mode WriteFile asked for, read back through a different header field
		// in the other direction.
		if got := file.Mode.Perm(); got != 0o640 {
			t.Errorf("main.go mode = %#o, want %#o", got, 0o640)
		}

		dir, ok := byPath["/work/list/logs"]
		if !ok {
			// A trailing slash surviving from the tar entry name lands here, and it
			// would make the path unusable in any other method.
			t.Fatalf("listing = %+v, want an entry for /work/list/logs with no trailing slash", got)
		}
		if !dir.IsDir || !dir.Mode.IsDir() {
			t.Errorf("logs IsDir = %v, Mode = %v, want a directory by both", dir.IsDir, dir.Mode)
		}
	})

	t.Run("a file lists itself", func(t *testing.T) {
		got, err := rt.ListDir(t.Context(), id, "/work/list/main.go")
		if err != nil {
			t.Fatalf("ListDir(file) error = %v, want nil", err)
		}
		if len(got) != 1 || got[0].Path != "/work/list/main.go" || got[0].IsDir {
			t.Fatalf("ListDir(file) = %+v, want one non-directory entry for the file itself", got)
		}
	})

	t.Run("a path that is not there", func(t *testing.T) {
		got, err := rt.ListDir(t.Context(), id, "/work/list/nowhere")
		if !errors.Is(err, runtime.ErrPathNotFound) {
			t.Errorf("ListDir(missing) error = %v, want ErrPathNotFound", err)
		}
		if got != nil {
			t.Errorf("ListDir(missing) = %+v, want no entries", got)
		}
	})
}

func writeOrFatal(t *testing.T, rt *runtime.DockerRuntime, id, path string, content []byte, mode fs.FileMode) {
	t.Helper()

	if err := rt.WriteFile(t.Context(), id, path, content, mode); err != nil {
		t.Fatalf("WriteFile(%s) error = %v, want nil", path, err)
	}
}

func indexByPath(infos []runtime.FileInfo) map[string]runtime.FileInfo {
	byPath := make(map[string]runtime.FileInfo, len(infos))
	for _, info := range infos {
		byPath[info.Path] = info
	}
	return byPath
}

// firstDifference reports the index of the first differing byte, or -1 if one
// slice is a prefix of the other.
func firstDifference(got, want []byte) int {
	for i := range min(len(got), len(want)) {
		if got[i] != want[i] {
			return i
		}
	}
	return -1
}
