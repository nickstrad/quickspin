package runtime

import (
	"errors"
	"io/fs"
	"path"
)

type FileInfo struct {
	Path  string
	Size  int64
	Mode  fs.FileMode
	IsDir bool
}

const MaxFileSize = 10 << 20 // 10 MiB
const MaxTotalFiles = 1000

var (
	ErrInvalidPath        = errors.New("invalid path")
	ErrPathNotFound       = errors.New("path not found in sandbox")
	ErrFileTooLarge       = errors.New("file exceeds file cap")
	ErrTotalFilesTooLarge = errors.New("total files exceeds file total cap")
)

func validatePath(p string) error {
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

// "/" passes validatePath but is not a readable or writable file target.
func validateRead(filePath string) error {
	if err := validatePath(filePath); err != nil {
		return err
	}
	if filePath == "/" {
		return ErrInvalidPath
	}
	return nil
}

func validateWrite(filePath string, content []byte) error {
	if err := validateRead(filePath); err != nil {
		return err
	}
	if len(content) > MaxFileSize {
		return ErrFileTooLarge
	}
	return nil
}
