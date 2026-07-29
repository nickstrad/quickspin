package runtime

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io/fs"
	"strings"
)

// The daemon chmods extracted directories — even pre-existing ones — so dir
// entries must keep execute bits or the parent becomes untraversable.
const archiveDirMode = 0o750

// filePath must already have passed validateWrite; a relative, unclean, or
// root path produces a malformed archive rather than an error.
func fileArchive(filePath string, content []byte, mode fs.FileMode) ([]byte, error) {
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)

	// Grow covers the 512-byte blocks: one per header, one content padding,
	// two trailer.
	name := filePath[1:]
	tokens := strings.Split(name, "/")
	archive.Grow(len(content) + 512*(len(tokens)+3))

	for i := range tokens[:len(tokens)-1] {
		if err := tw.WriteHeader(&tar.Header{
			Name:     strings.Join(tokens[:i+1], "/") + "/",
			Mode:     archiveDirMode,
			Typeflag: tar.TypeDir,
		}); err != nil {
			return nil, fmt.Errorf("creating tar header for %s: %w", filePath, err)
		}
	}

	if err := tw.WriteHeader(&tar.Header{
		Name:     name,
		Mode:     int64(mode.Perm()),
		Size:     int64(len(content)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		return nil, fmt.Errorf("creating tar header for %s: %w", filePath, err)
	}
	if _, err := tw.Write(content); err != nil {
		return nil, fmt.Errorf("writing tar content for %s: %w", filePath, err)
	}
	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("closing tar archive for %s: %w", filePath, err)
	}
	return archive.Bytes(), nil
}
