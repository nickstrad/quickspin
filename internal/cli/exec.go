package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/spf13/cobra"
)

// truncationWarning names the streams the runtime had to cut, or "" when both
// arrived whole. Surfacing it is the point of ExecResult carrying the flags at
// all: a truncated JSON document is indistinguishable from a malformed one, so
// a caller whose parse fails needs to be told which of the two happened.
//
// Receiver-free so the wording — the only thing a user actually reads here —
// can be table-tested without a runtime.
func truncationWarning(result runtime.ExecResult) string {
	var cut []string
	if result.StdoutTruncated {
		cut = append(cut, "stdout")
	}
	if result.StderrTruncated {
		cut = append(cut, "stderr")
	}
	if len(cut) == 0 {
		return ""
	}
	return fmt.Sprintf("%s truncated at %d bytes per stream", strings.Join(cut, " and "), runtime.MaxStreamBytes)
}

func (app *application) newExecCommand() *cobra.Command {
	var (
		env     []string
		workDir string
		timeout time.Duration
	)

	cmd := &cobra.Command{
		Use:   "exec ID -- COMMAND [ARG...]",
		Short: "Run a command inside a sandbox",
		// Everything after `--` is the sandbox's command, not quickspin's. Cobra
		// stops parsing flags at `--` by itself, so a `-e` meant for the container's
		// command is passed through rather than eaten as quickspin's --env.
		Args: cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, command := args[0], args[1:]
			app.logCommand(cmd, "sandboxID", id, "command", command, "timeout", timeout)

			// Distinct from create's --env: this reaches ExecCreateOptions.Env and
			// applies to this process alone, layered over the container's own
			// environment. Create's --env is baked into the container at build time
			// and inherited by everything in it.
			environment, err := parseEnvironment(env)
			if err != nil {
				return err
			}

			result, err := app.runtime.Exec(cmd.Context(), id, command, runtime.ExecOpts{
				Env:     environment,
				WorkDir: workDir,
				Timeout: timeout,
			})
			if err != nil {
				return fmt.Errorf("exec in sandbox %q: %w", id, err)
			}

			// The command's streams go to the CLI's streams unchanged rather than
			// through the renderer. `exec <id> -- cat /sys/fs/cgroup/memory.max` is
			// only useful if what comes back is the file's contents, and separating
			// the two streams is the whole point of the stdcopy demux upstream — the
			// CLI must not merge them back together.
			//
			// A failed write is reported rather than dropped: piping into a command
			// that exits early closes the pipe, and swallowing that turns "you got
			// none of the output" into an apparent success.
			if _, err := cmd.OutOrStdout().Write(result.Stdout); err != nil {
				return fmt.Errorf("writing stdout from sandbox %q: %w", id, err)
			}
			if _, err := cmd.ErrOrStderr().Write(result.Stderr); err != nil {
				return fmt.Errorf("writing stderr from sandbox %q: %w", id, err)
			}

			// After the streams, so it cannot be mistaken for part of them, and
			// prefixed because it is quickspin talking, not the command.
			if warning := truncationWarning(result); warning != "" {
				fmt.Fprintf(cmd.ErrOrStderr(), "quickspin: %s\n", warning)
			}

			if result.ExitCode != 0 {
				// Reported, not propagated: main exits 1 for any error, so a sandbox
				// command that exits 137 is currently indistinguishable from a
				// quickspin failure in $?. Carrying the code out to the process exit
				// status needs main to understand a typed exit error — worth settling
				// before the OOM check (exit 137) is driven from the CLI.
				return fmt.Errorf("command in sandbox %q exited %d", id, result.ExitCode)
			}
			return nil
		},
	}
	cmd.Flags().StringArrayVarP(
		&env,
		"env",
		"e",
		nil,
		"set an environment variable for this command only (KEY=VALUE); repeat for multiple values",
	)
	cmd.Flags().StringVarP(
		&workDir,
		"workdir",
		"w",
		"",
		"working directory for this command (default: the image's own)",
	)
	cmd.Flags().DurationVar(
		&timeout,
		"timeout",
		runtime.DefaultExecTimeout,
		"how long the command may run before it is cancelled (e.g. 5s, 2m)",
	)

	return cmd
}
