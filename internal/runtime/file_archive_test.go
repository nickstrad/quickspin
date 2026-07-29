package runtime

import (
	"archive/tar"
	"bytes"
	"errors"
	"io"
	"io/fs"
	"slices"
	"testing"
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
			wantErr: ErrPathNotFound,
		},
		{
			// A directory carrying the requested name is not the file: without the
			// Typeflag check its zero-size header would read as an empty file, and
			// the caller could not tell that apart from a real empty one.
			name:    "a directory sharing the name is not the file",
			path:    "/work/logs",
			archive: tarEntries(t, testTarEntry{tar.Header{Name: "logs/", Typeflag: tar.TypeDir, Mode: 0o755}, nil}),
			wantErr: ErrPathNotFound,
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
			wantErr: ErrPathNotFound,
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
			wantErr: ErrPathNotFound,
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
			wantErr: ErrPathNotFound,
		},
		{
			// Header-only: the size is a claim, and the point is that the claim
			// alone is refused. An archive that really carried the bytes would
			// allocate past the cap to prove the cap works.
			name:    "an oversized header is refused before the body is read",
			path:    "/work/core.dump",
			archive: headerOnlyTar(t, tar.Header{Name: "core.dump", Typeflag: tar.TypeReg, Size: MaxFileSize + 1}),
			wantErr: ErrFileTooLarge,
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
				if errors.Is(err, ErrPathNotFound) || errors.Is(err, ErrFileTooLarge) {
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
