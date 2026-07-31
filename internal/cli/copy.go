package cli

import (
	"errors"
	"fmt"
	"os"
	"path"
	"strings"

	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/spf13/cobra"
)

type sandboxPath struct {
	id   string
	path string
}

func parseSandboxPath(value string) (sandboxPath, bool) {
	id, filePath, ok := strings.Cut(value, ":")
	if !ok || id == "" || !path.IsAbs(filePath) {
		return sandboxPath{}, false
	}
	return sandboxPath{id: id, path: filePath}, true
}

func (app *application) newCopyCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "cp SOURCE DESTINATION",
		Short: "Copy a file into or out of a sandbox",
		Long: "Copy a file into or out of a sandbox.\n\n" +
			"Exactly one of SOURCE and DESTINATION is a sandbox path, written\n" +
			"ID:/absolute/path. The other is a local path. Copying in preserves the\n" +
			"local file's permission bits.",
		Example: `  ID=$(quickspin sandbox create alpine:3.20 -o json | jq -r .sandbox_id)

  # In, then back out.
  quickspin sandbox cp ./config.yaml "$ID":/app/config.yaml
  quickspin sandbox cp "$ID":/app/out.txt ./out.txt

  # Write a file without creating one locally first, using bash/zsh process
  # substitution. The pipe's mode comes along, so the file lands read-only.
  quickspin sandbox cp <(echo 'port: 8080') "$ID":/app/config.yaml`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			source, sourceInSandbox := parseSandboxPath(args[0])
			destination, destinationInSandbox := parseSandboxPath(args[1])
			if sourceInSandbox == destinationInSandbox {
				return errors.New("cp requires exactly one sandbox path in ID:/absolute/path form")
			}

			if sourceInSandbox {
				app.logCommand(cmd, "sandboxID", source.id, "source", source.path, "destination", args[1])
				content, err := app.api.ReadFile(cmd.Context(), source.id, source.path)
				if err != nil {
					return fmt.Errorf("copy %s from sandbox %q: %w", source.path, source.id, err)
				}
				if err := os.WriteFile(args[1], content, 0o644); err != nil {
					return fmt.Errorf("write local file %q: %w", args[1], err)
				}
				return nil
			}

			app.logCommand(cmd, "sandboxID", destination.id, "source", args[0], "destination", destination.path)
			info, err := os.Stat(args[0])
			if err != nil {
				return fmt.Errorf("stat local file %q: %w", args[0], err)
			}
			if info.Size() > runtime.MaxFileSize {
				return fmt.Errorf("copy local file %q: %w", args[0], runtime.ErrFileTooLarge)
			}
			content, err := os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("read local file %q: %w", args[0], err)
			}
			if err := app.api.WriteFile(
				cmd.Context(),
				destination.id,
				destination.path,
				content,
				info.Mode().Perm(),
			); err != nil {
				return fmt.Errorf("copy %q into sandbox %q: %w", args[0], destination.id, err)
			}
			return nil
		},
	}
}
