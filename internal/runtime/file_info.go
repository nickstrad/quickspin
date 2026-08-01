package runtime

import (
	"errors"
	"io/fs"
	"path"
)

type FileInfo struct {
	Path  string      `json:"path" yaml:"path"`
	Size  int64       `json:"size" yaml:"size"`
	Mode  fs.FileMode `json:"mode" yaml:"mode"`
	IsDir bool        `json:"is_dir" yaml:"is_dir"`
}

const MaxFileSize = 10 << 20 // 10 MiB
const MaxTotalFiles = 1000

var (
	ErrInvalidPath        = errors.New("invalid path")
	ErrPathNotFound       = errors.New("path not found in sandbox")
	ErrFileTooLarge       = errors.New("file exceeds file cap")
	ErrTotalFilesTooLarge = errors.New("total files exceeds file total cap")
)

func ValidatePath(p string) error {
	if p == "" {
		return ErrInvalidPath
	}
	if !path.IsAbs(p) {
		return ErrInvalidPath
	}
	if p != path.Clean(p) {
		return ErrInvalidPath
	}

	return nil
}

// "/" passes ValidatePath but is not a readable or writable file target.
func ValidateRead(filePath string) error {
	if err := ValidatePath(filePath); err != nil {
		return err
	}
	if filePath == "/" {
		return ErrInvalidPath
	}
	return nil
}

// A remove must never target the sandbox root; the shared not-root check is
// the guard that enforces it.
func ValidateRemove(p string) error {
	return ValidateRead(p)
}

func ValidateWrite(filePath string, content []byte) error {
	if err := ValidateRead(filePath); err != nil {
		return err
	}
	if len(content) > MaxFileSize {
		return ErrFileTooLarge
	}
	return nil
}
