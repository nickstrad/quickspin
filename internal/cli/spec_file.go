package cli

import (
	"errors"
	"fmt"
	"io"
	"maps"
	"os"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// execFile is the file form of an exec request. Pointers for the same reason as
// specFile, and Timeout is a string so it can hold a Go duration like "5s"
// rather than a bare count of some unstated unit.
//
// There is deliberately no id key: a sandbox ID is minted at create time, so a
// file naming one would only ever be right for a single sandbox.
type execFile struct {
	Command []string          `yaml:"command"`
	Env     map[string]string `yaml:"env"`
	WorkDir *string           `yaml:"workdir"`
	Timeout *string           `yaml:"timeout"`
}

// addSpecFileFlag registers --file on a command that can take its inputs as a
// file, so the accepted extensions and the precedence wording stay in one place.
func addSpecFileFlag(cmd *cobra.Command, path *string, usage string) {
	cmd.Flags().StringVarP(path, "file", "f", "", usage)
	cmd.MarkFlagFilename("file", "yaml", "yml", "json")
}

// loadFile reads a spec file, accepting YAML or JSON without being told which:
// YAML 1.2 is a superset of JSON, so the YAML parser reads both and there is
// nothing to sniff and no extension to trust.
//
// An empty path means no --file was given and yields the zero T, so every field
// falls through to its flag or default.
func loadFile[T any](path string) (T, error) {
	var zero, file T

	if path == "" {
		return zero, nil
	}

	handle, err := os.Open(path)
	if err != nil {
		return zero, fmt.Errorf("open spec file: %w", err)
	}
	defer handle.Close()

	decoder := yaml.NewDecoder(handle)
	// An unknown key is an error rather than a silent no-op: a `memoy: 4g` that
	// parsed cleanly would produce a sandbox with the default limit and no
	// indication the file was ignored.
	decoder.KnownFields(true)

	if err := decoder.Decode(&file); err != nil {
		if errors.Is(err, io.EOF) {
			err = errors.New("file is empty")
		}
		return zero, fmt.Errorf("read spec file %q: %w", path, err)
	}

	// yaml.Decoder reads one document per call, so a `---`-separated file would
	// otherwise have everything after the first document dropped without a word.
	if err := decoder.Decode(new(T)); !errors.Is(err, io.EOF) {
		return zero, fmt.Errorf("read spec file %q: file has more than one document; expected exactly one", path)
	}

	return file, nil
}

// resolve returns the winning value for one field across the three sources, in
// increasing order of precedence: the built-in default, the spec file, the flag.
// flagSet must come from Flags().Changed, since a flag left alone is
// indistinguishable from one set to its default value.
func resolve[T any](fallback T, fromFile *T, flagSet bool, fromFlag T) T {
	if fromFile != nil {
		fallback = *fromFile
	}
	if flagSet {
		fallback = fromFlag
	}
	return fallback
}

// resolveEnvironment overlays --env variables onto the file's per key, so a
// single -e overrides one variable instead of discarding the file's whole env
// block. Returns nil when neither source names anything, matching what
// parseEnvironment gives back for no --env at all.
func resolveEnvironment(fromFile map[string]string, flagValues []string) (map[string]string, error) {
	fromFlags, err := parseEnvironment(flagValues)
	if err != nil {
		return nil, err
	}

	if len(fromFile) == 0 && len(fromFlags) == 0 {
		return nil, nil
	}

	merged := make(map[string]string, len(fromFile)+len(fromFlags))
	maps.Copy(merged, fromFile)
	maps.Copy(merged, fromFlags)
	return merged, nil
}
