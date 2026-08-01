package docker

import (
	"archive/tar"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"
	"testing"

	"github.com/nickstrad/quickspin/internal/runtime"
)

// testBinaryContent holds bytes that are not valid UTF-8, so any stray string
// conversion in an archive path corrupts it visibly.
var testBinaryContent = []byte{0x00, 0xff, 'q', 'u', 'i', 'c', 'k', '\n'}

func TestFileArchive(t *testing.T) {

	tests := []struct {
		name    string
		path    string
		content []byte
		mode    fs.FileMode
		// wantNames lists every entry in order; a trailing slash marks a
		// directory entry.
		wantNames    []string
		wantFileMode int64
	}{
		{
			name:         "nested path creates parent directories",
			path:         "/work/a/b/main.bin",
			content:      testBinaryContent,
			mode:         0o640,
			wantNames:    []string{"work/", "work/a/", "work/a/b/", "work/a/b/main.bin"},
			wantFileMode: 0o640,
		},
		{
			name:         "file at container root has no parents",
			path:         "/README.md",
			content:      []byte("hello"),
			mode:         0o644,
			wantNames:    []string{"README.md"},
			wantFileMode: 0o644,
		},
		{
			name:         "empty file is preserved",
			path:         "/work/empty.txt",
			content:      nil,
			mode:         0o600,
			wantNames:    []string{"work/", "work/empty.txt"},
			wantFileMode: 0o600,
		},
		{
			name:         "fs.FileMode type bits stay out of the header",
			path:         "/work/tool",
			content:      []byte("#!/bin/sh\n"),
			mode:         0o750 | fs.ModeSetgid,
			wantNames:    []string{"work/", "work/tool"},
			wantFileMode: 0o750,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			archive, err := fileArchive(tt.path, tt.content, tt.mode)
			if err != nil {
				t.Fatalf("fileArchive error = %v, want nil", err)
			}

			entries := readTarEntries(t, archive)
			if got := tarEntryNames(entries); !slices.Equal(got, tt.wantNames) {
				t.Fatalf("tar entries = %v, want %v", got, tt.wantNames)
			}

			for _, entry := range entries[:len(entries)-1] {
				if entry.header.Typeflag != tar.TypeDir {
					t.Errorf("dir entry %q type = %d, want directory", entry.header.Name, entry.header.Typeflag)
				}
				if entry.header.Mode != archiveDirMode {
					t.Errorf("dir entry %q mode = %#o, want %#o", entry.header.Name, entry.header.Mode, int64(archiveDirMode))
				}
				if entry.header.Size != 0 {
					t.Errorf("dir entry %q size = %d, want 0", entry.header.Name, entry.header.Size)
				}
			}

			file := entries[len(entries)-1]
			if file.header.Typeflag != tar.TypeReg {
				t.Errorf("file entry type = %d, want regular file", file.header.Typeflag)
			}
			if file.header.Mode != tt.wantFileMode {
				t.Errorf("file entry mode = %#o, want %#o", file.header.Mode, tt.wantFileMode)
			}
			if file.header.Size != int64(len(tt.content)) {
				t.Errorf("file entry size = %d, want %d", file.header.Size, len(tt.content))
			}
			if !bytes.Equal(file.content, tt.content) {
				t.Errorf("file entry content = %v, want %v", file.content, tt.content)
			}
		})
	}
}

func TestFileUnarchive(t *testing.T) {
	// fileArchive's output is deliberately not fed back in here. The two sides
	// name entries differently — fileArchive writes the full path from the
	// container root ("work/a/b/main.bin") because CopyToContainer extracts
	// relative to "/", while CopyFromContainer names entries relative to the
	// source's parent ("main.bin"). The round trip closes through the daemon,
	// not through these two functions, so the live suite is what proves it.

	tests := []struct {
		name    string
		path    string
		archive []byte
		want    []byte
		// wantErr is matched with errors.Is; wantPlainErr demands an error
		// carrying no sentinel at all, for the malformed streams whose exact
		// wording is not part of the contract.
		wantErr      error
		wantPlainErr bool
	}{
		{
			// The shape the daemon actually returns for a file source: one entry
			// named for the basename, with none of the parents the request path had.
			name:    "docker's single basename entry",
			path:    "/work/a/b/main.bin",
			archive: tarEntries(t, testTarEntry{tar.Header{Name: "main.bin", Typeflag: tar.TypeReg, Mode: 0o640, Size: int64(len(testBinaryContent))}, testBinaryContent}),
			want:    testBinaryContent,
		},
		{
			// A file source can still bring directory entries along, and they must
			// be walked past rather than mistaken for the file.
			name: "parent directory entries are walked past",
			path: "/work/a/b/main.bin",
			archive: tarEntries(t,
				testTarEntry{tar.Header{Name: "b/", Typeflag: tar.TypeDir, Mode: 0o755}, nil},
				testTarEntry{tar.Header{Name: "main.bin", Typeflag: tar.TypeReg, Mode: 0o640, Size: int64(len(testBinaryContent))}, testBinaryContent},
			),
			want: testBinaryContent,
		},
		{
			name:    "an empty file is not a missing file",
			path:    "/work/empty.txt",
			archive: tarEntries(t, testTarEntry{tar.Header{Name: "empty.txt", Typeflag: tar.TypeReg, Mode: 0o600, Size: 0}, nil}),
			want:    []byte{},
		},
		{
			name:    "no entry matches",
			path:    "/work/main.bin",
			archive: tarEntries(t, testTarEntry{tar.Header{Name: "other.bin", Typeflag: tar.TypeReg, Size: 3}, []byte("abc")}),
			wantErr: runtime.ErrPathNotFound,
		},
		{
			// A directory carrying the requested name is not the file: without the
			// Typeflag check its zero-size header would read as an empty file, and
			// the caller could not tell that apart from a real empty one.
			name:    "a directory sharing the name is not the file",
			path:    "/work/logs",
			archive: tarEntries(t, testTarEntry{tar.Header{Name: "logs/", Typeflag: tar.TypeDir, Mode: 0o755}, nil}),
			wantErr: runtime.ErrPathNotFound,
		},
		{
			// Entry names are relative to the source, so a file source yields
			// exactly "main.go". An entry nested under a directory is a different
			// file that happens to share a basename, and returning its bytes as the
			// requested file's is a silent wrong answer — the worst failure this
			// function has, because nothing downstream can detect it.
			name:    "a same-named file in another directory is not the file",
			path:    "/work/main.go",
			archive: tarEntries(t, testTarEntry{tar.Header{Name: "vendor/main.go", Typeflag: tar.TypeReg, Size: 5}, []byte("wrong")}),
			wantErr: runtime.ErrPathNotFound,
		},
		{
			// Reading a directory returns its whole tree, and no entry is the
			// requested file. Bytes must not be invented from a child.
			name: "a directory source yields no file",
			path: "/work/a",
			archive: tarEntries(t,
				testTarEntry{tar.Header{Name: "a/", Typeflag: tar.TypeDir, Mode: 0o755}, nil},
				testTarEntry{tar.Header{Name: "a/main.go", Typeflag: tar.TypeReg, Size: 5}, []byte("child")},
			),
			wantErr: runtime.ErrPathNotFound,
		},
		{
			// Entry names are matched exactly, not by basename: a nested
			// namesake must not be returned as the directory's content.
			name: "a nested file sharing the basename is not the file",
			path: "/work/config",
			archive: tarEntries(t,
				testTarEntry{tar.Header{Name: "config/", Typeflag: tar.TypeDir, Mode: 0o755}, nil},
				testTarEntry{tar.Header{Name: "config/nested/", Typeflag: tar.TypeDir, Mode: 0o755}, nil},
				testTarEntry{tar.Header{Name: "config/nested/config", Typeflag: tar.TypeReg, Mode: 0o640, Size: int64(len(testBinaryContent))}, testBinaryContent},
			),
			wantErr: runtime.ErrPathNotFound,
		},
		{
			// Header-only: the size is a claim, and the point is that the claim
			// alone is refused. An archive that really carried the bytes would
			// allocate past the cap to prove the cap works.
			name:    "an oversized header is refused before the body is read",
			path:    "/work/core.dump",
			archive: headerOnlyTar(t, tar.Header{Name: "core.dump", Typeflag: tar.TypeReg, Size: runtime.MaxFileSize + 1}),
			wantErr: runtime.ErrFileTooLarge,
		},
		{
			// A stream that ends mid-body must not report the sentinel a caller
			// treats as "the file is not there": the file is there and the transfer
			// broke, which is a retry, not a 404.
			name:         "a truncated body is a transfer failure, not an absence",
			path:         "/work/main.bin",
			archive:      headerOnlyTar(t, tar.Header{Name: "main.bin", Typeflag: tar.TypeReg, Size: 512}),
			wantPlainErr: true,
		},
		{
			name:         "a stream that is not a tar archive",
			path:         "/work/main.bin",
			archive:      []byte("this is not a tar archive, it is a sentence"),
			wantPlainErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := fileUnarchive(tt.path, io.NopCloser(bytes.NewReader(tt.archive)))

			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("fileUnarchive error = %v, want errors.Is(..., %v)", err, tt.wantErr)
				}
			case tt.wantPlainErr:
				if err == nil {
					t.Fatal("fileUnarchive error = nil, want an error for a malformed stream")
				}
				// The sentinels are what callers branch on, so a broken stream
				// leaking one of them is worse than the bare error.
				if errors.Is(err, runtime.ErrPathNotFound) || errors.Is(err, runtime.ErrFileTooLarge) {
					t.Errorf("fileUnarchive error = %v, want no sentinel for a malformed stream", err)
				}
			default:
				if err != nil {
					t.Fatalf("fileUnarchive error = %v, want nil", err)
				}
				if !bytes.Equal(got, tt.want) {
					t.Errorf("fileUnarchive = %v, want %v", got, tt.want)
				}
			}
			if err != nil && got != nil {
				t.Errorf("fileUnarchive returned %d bytes alongside %v, want no partial content", len(got), err)
			}
		})
	}
}

// The daemon names archive entries relative to the *parent* of the source, so
// every case below writes names as Docker would send them ("work/main.go" for a
// source of /work) and expects paths the caller can hand straight back to
// ReadFile.
//
// The listing is the whole subtree: the archive already carries every
// descendant, so filtering to one level would pay the wire cost and discard the
// result. MaxTotalFiles is what bounds it — see
// TestListDirectoryFromTarStreamCapsEntries.
func TestListDirectoryFromTarStream(t *testing.T) {
	tests := []struct {
		name    string
		dirPath string
		archive []byte
		want    []runtime.FileInfo
		// wantPlainErr demands an error carrying no sentinel, for the malformed
		// streams whose exact wording is not part of the contract.
		wantPlainErr bool
	}{
		{
			name:    "a shallow directory's entries",
			dirPath: "/work",
			archive: tarEntries(t,
				testTarEntry{tar.Header{Name: "work/", Typeflag: tar.TypeDir, Mode: 0o755}, nil},
				testTarEntry{tar.Header{Name: "work/main.go", Typeflag: tar.TypeReg, Mode: 0o640, Size: 13}, []byte("package main\n")},
				testTarEntry{tar.Header{Name: "work/logs/", Typeflag: tar.TypeDir, Mode: 0o750}, nil},
			),
			want: []runtime.FileInfo{
				// No trailing slash: the slash is tar's way of marking a directory
				// entry, and IsDir already carries that. A path ending in "/" fails
				// runtime.ValidatePath, so leaving it on would produce entries that cannot be
				// passed back into any other method.
				{Path: "/work/logs", Mode: fs.ModeDir | 0o750, IsDir: true},
				{Path: "/work/main.go", Size: 13, Mode: 0o640},
			},
		},
		{
			// The source directory is in its own archive, and reporting it as an
			// entry makes a listing of an empty directory indistinguishable from one
			// holding a single subdirectory. This is the one entry the recursion
			// skips, and it is skipped because it is the question, not an answer.
			name:    "the source directory is not one of its own entries",
			dirPath: "/work/empty",
			archive: tarEntries(t,
				testTarEntry{tar.Header{Name: "empty/", Typeflag: tar.TypeDir, Mode: 0o755}, nil},
			),
			want: []runtime.FileInfo{},
		},
		{
			// Every depth, not just one level: the join is the same at any depth, so a
			// grandchild's path must come back as usable as a child's. Absolute paths
			// are what make a flat listing of a deep tree readable at all — the
			// alternative is a set of basenames with no way to tell two "main.go"
			// apart.
			name:    "the whole subtree is listed",
			dirPath: "/work",
			archive: tarEntries(t,
				testTarEntry{tar.Header{Name: "work/", Typeflag: tar.TypeDir, Mode: 0o755}, nil},
				testTarEntry{tar.Header{Name: "work/top.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: 5}, []byte("hello")},
				testTarEntry{tar.Header{Name: "work/a/", Typeflag: tar.TypeDir, Mode: 0o755}, nil},
				testTarEntry{tar.Header{Name: "work/a/b/", Typeflag: tar.TypeDir, Mode: 0o750}, nil},
				testTarEntry{tar.Header{Name: "work/a/b/c.txt", Typeflag: tar.TypeReg, Mode: 0o600, Size: 3}, []byte("abc")},
			),
			want: []runtime.FileInfo{
				{Path: "/work/a", Mode: fs.ModeDir | 0o755, IsDir: true},
				{Path: "/work/a/b", Mode: fs.ModeDir | 0o750, IsDir: true},
				{Path: "/work/a/b/c.txt", Size: 3, Mode: 0o600},
				{Path: "/work/top.txt", Size: 5, Mode: 0o644},
			},
		},
		{
			// `ls file` prints the file. The daemon answers a file source with a
			// single basename entry, so the same rule that skips "work/" for a
			// directory source must not skip "main.go" here.
			name:    "a file source lists the file itself",
			dirPath: "/work/main.go",
			archive: tarEntries(t,
				testTarEntry{tar.Header{Name: "main.go", Typeflag: tar.TypeReg, Mode: 0o640, Size: 13}, []byte("package main\n")},
			),
			want: []runtime.FileInfo{
				{Path: "/work/main.go", Size: 13, Mode: 0o640},
			},
		},
		{
			// A stream that dies mid-entry has produced a partial listing, and a
			// partial listing returned without an error is a wrong answer: the caller
			// concludes the missing files do not exist.
			name:         "a truncated stream is not a short listing",
			dirPath:      "/work",
			archive:      headerOnlyTar(t, tar.Header{Name: "work/main.go", Typeflag: tar.TypeReg, Mode: 0o644, Size: 512}),
			wantPlainErr: true,
		},
		{
			name:         "a stream that is not a tar archive",
			dirPath:      "/work",
			archive:      []byte("this is not a tar archive, it is a sentence"),
			wantPlainErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := listDirectoryFromTarStream(tt.dirPath, io.NopCloser(bytes.NewReader(tt.archive)))

			if tt.wantPlainErr {
				if err == nil {
					t.Fatal("listDirectoryFromTarStream error = nil, want an error for a malformed stream")
				}
				if errors.Is(err, runtime.ErrPathNotFound) || errors.Is(err, runtime.ErrTotalFilesTooLarge) {
					t.Errorf("listDirectoryFromTarStream error = %v, want no sentinel for a malformed stream", err)
				}
				if got != nil {
					t.Errorf("listDirectoryFromTarStream returned %d entries alongside %v, want no partial listing", len(got), err)
				}
				return
			}

			if err != nil {
				t.Fatalf("listDirectoryFromTarStream error = %v, want nil", err)
			}
			// An empty directory is an empty listing, not a nil one: plan 05 serves
			// these over HTTP, where nil encodes as `null` and an empty slice as `[]`.
			if got == nil {
				t.Fatal("listDirectoryFromTarStream = nil, want an empty slice for a directory with no children")
			}
			assertFileInfos(t, got, tt.want)
		})
	}
}

// Because the listing recurses, MaxTotalFiles is the only thing standing between
// a caller and a listing of every file in the sandbox. It therefore counts
// listed entries at any depth — a deep tree cannot hide entries from it by
// nesting them — and the refusal is a sentinel rather than a truncated slice,
// since a silently short listing reads as "these are all the files".
func TestListDirectoryFromTarStreamCapsEntries(t *testing.T) {
	tests := []struct {
		name string
		// names are tar entry names; a trailing slash makes a directory entry.
		names     []string
		wantCount int
		wantErr   error
	}{
		{
			name:      "exactly at the cap",
			names:     append([]string{"work/"}, generatedNames("work/f%04d.txt", runtime.MaxTotalFiles)...),
			wantCount: runtime.MaxTotalFiles,
		},
		{
			name:    "one over the cap",
			names:   append([]string{"work/"}, generatedNames("work/f%04d.txt", runtime.MaxTotalFiles+1)...),
			wantErr: runtime.ErrTotalFilesTooLarge,
		},
		{
			// 999 files is comfortably under the cap on its own; the two directories
			// holding them are listed too, and that is what pushes the total over. A
			// cap that counted only the deepest entries, or only files, would let a
			// sufficiently nested tree past.
			name: "nesting does not exempt entries from the cap",
			names: append([]string{"work/", "work/a/", "work/a/b/"},
				generatedNames("work/a/b/f%04d.txt", runtime.MaxTotalFiles-1)...),
			wantErr: runtime.ErrTotalFilesTooLarge,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			archive := tarNamedEntries(t, tt.names...)

			got, err := listDirectoryFromTarStream("/work", io.NopCloser(bytes.NewReader(archive)))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("listDirectoryFromTarStream error = %v, want errors.Is(..., %v)", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				// Refusing with a partial listing attached invites a caller to use it.
				if got != nil {
					t.Errorf("listDirectoryFromTarStream returned %d entries alongside %v, want none", len(got), err)
				}
				return
			}
			if len(got) != tt.wantCount {
				t.Errorf("listDirectoryFromTarStream returned %d entries, want %d", len(got), tt.wantCount)
			}
		})
	}
}

func generatedNames(format string, n int) []string {
	names := make([]string, n)
	for i := range names {
		names[i] = fmt.Sprintf(format, i)
	}
	return names
}

// tarNamedEntries builds an archive of empty entries from names alone, for the
// cases that care about how many entries there are rather than what is in them.
// A trailing slash makes a directory entry.
func tarNamedEntries(t *testing.T, names ...string) []byte {
	t.Helper()

	entries := make([]testTarEntry, 0, len(names))
	for _, name := range names {
		header := tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644}
		if strings.HasSuffix(name, "/") {
			header.Typeflag, header.Mode = tar.TypeDir, 0o755
		}
		entries = append(entries, testTarEntry{header, nil})
	}
	return tarEntries(t, entries...)
}

// MaxFileSize bounds what ReadFile pulls into memory. A listing reads headers
// only, so the same cap applied here would make a directory unlistable because
// of one core dump sitting in it — the entry a caller most needs to see.
func TestListDirectoryFromTarStreamReportsSizesOverTheFileCap(t *testing.T) {
	oversized := make([]byte, runtime.MaxFileSize+1)
	archive := tarEntries(t,
		testTarEntry{tar.Header{Name: "work/", Typeflag: tar.TypeDir, Mode: 0o755}, nil},
		testTarEntry{tar.Header{Name: "work/core.dump", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(oversized))}, oversized},
	)

	got, err := listDirectoryFromTarStream("/work", io.NopCloser(bytes.NewReader(archive)))
	if err != nil {
		t.Fatalf("listDirectoryFromTarStream error = %v, want nil", err)
	}
	assertFileInfos(t, got, []runtime.FileInfo{
		{Path: "/work/core.dump", Size: int64(len(oversized)), Mode: 0o644},
	})
}

// Order is not part of the contract — the daemon's traversal order is its own
// business — so both sides are sorted before comparison.
func assertFileInfos(t *testing.T, got, want []runtime.FileInfo) {
	t.Helper()

	byPath := func(a, b runtime.FileInfo) int { return strings.Compare(a.Path, b.Path) }
	got = slices.SortedFunc(slices.Values(got), byPath)
	want = slices.SortedFunc(slices.Values(want), byPath)

	if !slices.Equal(got, want) {
		t.Fatalf("listing = %+v, want %+v", got, want)
	}
}

// tarEntries builds a well-formed archive. Each Header.Size is left as the
// caller wrote it rather than derived from content, so a test can state a size
// the bytes do not back.
func tarEntries(t *testing.T, entries ...testTarEntry) []byte {
	t.Helper()

	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, e := range entries {
		if err := tw.WriteHeader(&e.header); err != nil {
			t.Fatalf("write tar header %q: %v", e.header.Name, err)
		}
		if _, err := tw.Write(e.content); err != nil {
			t.Fatalf("write tar content %q: %v", e.header.Name, err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	return buf.Bytes()
}

// headerOnlyTar stops after the header block, which tar.Writer emits eagerly.
// That is what lets a test declare a body it never pays to produce — a 10 MiB
// claim costs 512 bytes here.
func headerOnlyTar(t *testing.T, header tar.Header) []byte {
	t.Helper()

	var buf bytes.Buffer
	if err := tar.NewWriter(&buf).WriteHeader(&header); err != nil {
		t.Fatalf("write tar header %q: %v", header.Name, err)
	}
	return buf.Bytes()
}

// assertFinalizedTar checks for the end-of-archive blocks at the byte level
// because tar.Reader cannot: an unclosed writer whose content ends on a block
// boundary still parses cleanly, so a reader-side check would miss it.
func assertFinalizedTar(t *testing.T, archive []byte) {
	t.Helper()

	const trailerSize = 2 * 512
	if len(archive) < trailerSize ||
		!bytes.Equal(archive[len(archive)-trailerSize:], make([]byte, trailerSize)) {
		t.Error("archive has no tar end-of-archive blocks; close the tar writer before returning")
	}
}

type testTarEntry struct {
	header  tar.Header
	content []byte
}

func readTarEntries(t *testing.T, archive []byte) []testTarEntry {
	t.Helper()
	assertFinalizedTar(t, archive)

	reader := tar.NewReader(bytes.NewReader(archive))
	var entries []testTarEntry
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			return entries
		}
		if err != nil {
			t.Fatalf("read tar header: %v", err)
		}

		content, err := io.ReadAll(reader)
		if err != nil {
			t.Fatalf("read tar entry %q: %v", header.Name, err)
		}
		entries = append(entries, testTarEntry{
			header:  *header,
			content: content,
		})
	}
}

func tarEntryNames(entries []testTarEntry) []string {
	names := make([]string, len(entries))
	for i, entry := range entries {
		names[i] = entry.header.Name
	}
	return names
}
