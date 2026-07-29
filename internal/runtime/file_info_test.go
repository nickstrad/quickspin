package runtime

import (
	"errors"
	"testing"
)

func TestValidatePath(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		wantErr error
	}{
		{name: "root", path: "/"},
		{name: "absolute directory", path: "/work"},
		{name: "absolute file", path: "/work/main.go"},
		{name: "deep path", path: "/work/a/b/c.txt"},
		{name: "empty", path: "", wantErr: ErrInvalidPath},
		{name: "relative", path: "work/main.go", wantErr: ErrInvalidPath},
		{name: "current directory", path: ".", wantErr: ErrInvalidPath},
		{name: "dot segment", path: "/work/./main.go", wantErr: ErrInvalidPath},
		{name: "repeated separator", path: "/work//main.go", wantErr: ErrInvalidPath},
		{name: "trailing separator", path: "/work/", wantErr: ErrInvalidPath},
		{name: "relative traversal", path: "../x", wantErr: ErrInvalidPath},
		{name: "nested relative traversal", path: "a/../../x", wantErr: ErrInvalidPath},
		{name: "traversal above root", path: "/../x", wantErr: ErrInvalidPath},
		{name: "traversal through parent", path: "/a/../../x", wantErr: ErrInvalidPath},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validatePath(tt.path); !errors.Is(err, tt.wantErr) {
				t.Errorf("validatePath(%q) error = %v, want %v", tt.path, err, tt.wantErr)
			}
		})
	}
}

func TestValidateWrite(t *testing.T) {
	tests := []struct {
		name    string
		path    string
		content []byte
		wantErr error
	}{
		{name: "content exactly at the size cap", path: "/work/big.bin", content: make([]byte, MaxFileSize)},
		{name: "root is not a writable file target", path: "/", wantErr: ErrInvalidPath},
		{name: "invalid path rejected before the size check", path: "work/main.go", content: make([]byte, MaxFileSize+1), wantErr: ErrInvalidPath},
		{name: "content over the size cap", path: "/work/big.bin", content: make([]byte, MaxFileSize+1), wantErr: ErrFileTooLarge},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := validateWrite(tt.path, tt.content); !errors.Is(err, tt.wantErr) {
				t.Errorf("validateWrite(%q, %d bytes) error = %v, want %v", tt.path, len(tt.content), err, tt.wantErr)
			}
		})
	}
}
