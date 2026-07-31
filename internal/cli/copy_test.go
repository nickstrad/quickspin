package cli_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/nickstrad/quickspin/internal/runtime"
)

func TestCopyUploadsLocalBytesAndMode(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "main.go")
	content := []byte("package main\n")
	if err := os.WriteFile(localPath, content, 0o600); err != nil {
		t.Fatalf("write source file: %v", err)
	}
	// Chmod, not the WriteFile perm, sets the mode: WriteFile's perm is umask-filtered.
	if err := os.Chmod(localPath, 0o640); err != nil {
		t.Fatalf("chmod source file: %v", err)
	}

	var (
		gotID      string
		gotPath    string
		gotContent []byte
		gotMode    fs.FileMode
	)
	api := fakeAPI{
		WriteFileFn: func(_ context.Context, id, path string, content []byte, mode fs.FileMode) error {
			gotID, gotPath, gotContent, gotMode = id, path, bytes.Clone(content), mode
			return nil
		},
	}

	stdout, _, err := execute(t, api, "sandbox", "cp", localPath, testID+":/work/main.go")
	if err != nil {
		t.Fatalf("execute cp upload error = %v, want nil", err)
	}
	if stdout != "" {
		t.Errorf("cp upload output = %q, want silent success", stdout)
	}
	if gotID != testID || gotPath != "/work/main.go" {
		t.Errorf("WriteFile target = %q:%s, want %q:/work/main.go", gotID, gotPath, testID)
	}
	if !bytes.Equal(gotContent, content) {
		t.Errorf("WriteFile content = %q, want %q", gotContent, content)
	}
	if gotMode != 0o640 {
		t.Errorf("WriteFile mode = %#o, want %#o", gotMode, fs.FileMode(0o640))
	}
}

func TestCopyDownloadsSandboxBytes(t *testing.T) {
	content := []byte{0x00, 0xff, 0x10, '\n'}
	var gotID, gotPath string
	api := fakeAPI{
		ReadFileFn: func(_ context.Context, id, path string) ([]byte, error) {
			gotID, gotPath = id, path
			return content, nil
		},
	}
	localPath := filepath.Join(t.TempDir(), "artifact.bin")

	if _, _, err := execute(t, api, "sandbox", "cp", testID+":/work/artifact.bin", localPath); err != nil {
		t.Fatalf("execute cp download error = %v, want nil", err)
	}
	if gotID != testID || gotPath != "/work/artifact.bin" {
		t.Errorf("ReadFile source = %q:%s, want %q:/work/artifact.bin", gotID, gotPath, testID)
	}
	got, err := os.ReadFile(localPath)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("downloaded content = %v, want %v", got, content)
	}
}

func TestCopyRefusesAnOversizedLocalFileBeforeTheRuntime(t *testing.T) {
	localPath := filepath.Join(t.TempDir(), "too-big.bin")
	file, err := os.Create(localPath)
	if err != nil {
		t.Fatalf("create source file: %v", err)
	}
	if err := file.Truncate(runtime.MaxFileSize + 1); err != nil {
		file.Close()
		t.Fatalf("truncate source file: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close source file: %v", err)
	}

	called := false
	api := fakeAPI{
		WriteFileFn: func(context.Context, string, string, []byte, fs.FileMode) error {
			called = true
			return nil
		},
	}

	_, _, err = execute(t, api, "sandbox", "cp", localPath, testID+":/work/too-big.bin")
	if !errors.Is(err, runtime.ErrFileTooLarge) {
		t.Fatalf("execute cp oversized error = %v, want ErrFileTooLarge", err)
	}
	if called {
		t.Error("WriteFile was called for a local file already known to exceed the cap")
	}
}

func TestCopyRequiresExactlyOneSandboxPath(t *testing.T) {
	tests := [][]string{
		{"local-a", "local-b"},
		{testID + ":/work/a", otherID + ":/work/b"},
	}
	for _, args := range tests {
		_, _, err := execute(t, fakeAPI{}, "sandbox", "cp", args[0], args[1])
		if err == nil {
			t.Errorf("cp %q %q error = nil, want an ambiguous-direction error", args[0], args[1])
		}
	}
}
