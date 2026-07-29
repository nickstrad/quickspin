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

func TestFileArchive(t *testing.T) {
	binaryContent := []byte{0x00, 0xff, 'q', 'u', 'i', 'c', 'k', '\n'}

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
			content:      binaryContent,
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
