package cli

import (
	"errors"
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

// execFlags collects what the exec flags carry, so resolveExec can be tested
// without building a cobra command.
type execFlags struct {
	env     []string
	workDir string
	timeout time.Duration
}

// argCommand is everything after ID, which cobra has already separated from
// quickspin's own flags. Empty is allowed only because the spec file can carry
// the command instead.
func resolveExec(
	argCommand []string,
	file execFile,
	flags execFlags,
	flagSet func(name string) bool,
) ([]string, runtime.ExecOpts, error) {
	command := argCommand
	if len(command) == 0 {
		command = file.Command
	}
	if len(command) == 0 {
		return nil, runtime.ExecOpts{}, errors.New("no command: pass it after -- or set command in the spec file")
	}

	// Distinct from create's --env: this reaches ExecCreateOptions.Env and applies
	// to this process alone, layered over the container's own environment.
	// Create's --env is baked into the container at build time and inherited by
	// everything in it.
	environment, err := resolveEnvironment(file.Env, flags.env)
	if err != nil {
		return nil, runtime.ExecOpts{}, err
	}

	// Parsed before resolve rather than inside it, since resolve works on a value
	// of the field's own type and the file spells a timeout as a duration string.
	var fileTimeout *time.Duration
	if file.Timeout != nil {
		parsed, err := time.ParseDuration(*file.Timeout)
		if err != nil {
			return nil, runtime.ExecOpts{}, fmt.Errorf("invalid timeout %q: expected a duration like 5s or 2m", *file.Timeout)
		}
		fileTimeout = &parsed
	}

	return command, runtime.ExecOpts{
		Env:     environment,
		WorkDir: resolve("", file.WorkDir, flagSet("workdir"), flags.workDir),
		Timeout: resolve(runtime.DefaultExecTimeout, fileTimeout, flagSet("timeout"), flags.timeout),
	}, nil
}

func (app *application) newExecCommand() *cobra.Command {
	var (
		specPath string
		flags    execFlags
	)

	cmd := &cobra.Command{
		Use:   "exec ID [-- COMMAND [ARG...]]",
		Short: "Run a command inside a sandbox",
		Long: "Run a command inside a sandbox.\n\n" +
			"The command and its options may come from flags, from a --file spec, or\n" +
			"from both. The file may be YAML or JSON; its keys match the flag names.\n" +
			"Flags win over the file, and --env merges into the file's env one\n" +
			"variable at a time. The file has no id key; ID is always an argument.",
		Example: `  ID=$(quickspin sandbox create alpine:3.20 -o json | jq -r .sandbox_id)

  # Everything after -- belongs to the sandbox, its own flags included.
  quickspin sandbox exec "$ID" -- sh -c 'echo hello'

  # Read back the cgroup files the create limits actually land in.
  quickspin sandbox exec "$ID" -- cat /sys/fs/cgroup/memory.max

  # Environment, working directory, and deadline for this command only.
  quickspin sandbox exec "$ID" -e MODE=debug -w /tmp --timeout 5s -- env

  # The same request as a spec file. Keys match the flag names, and the file
  # may be YAML or JSON. Runnable as written with a bash/zsh heredoc; the EOF
  # terminator must sit at the start of its own line.
  quickspin sandbox exec "$ID" -f <(cat <<'EOF'
command: [sh, -c, echo hello]
workdir: /tmp
timeout: 5s
env:
  MODE: debug
EOF
  )

  # Stdin is not forwarded to the command; write the file with cp instead.
  quickspin sandbox cp <(echo 'echo hello') "$ID":/tmp/run.sh
  quickspin sandbox exec "$ID" -- sh /tmp/run.sh`,
		// Everything after `--` is the sandbox's command, not quickspin's. Cobra
		// stops parsing flags at `--` by itself, so a `-e` meant for the container's
		// command is passed through rather than eaten as quickspin's --env.
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			file, err := loadFile[execFile](specPath)
			if err != nil {
				return err
			}

			id := args[0]
			command, opts, err := resolveExec(args[1:], file, flags, cmd.Flags().Changed)
			if err != nil {
				return err
			}
			app.logCommand(cmd, "sandboxID", id, "command", command, "timeout", opts.Timeout, "specFile", specPath)

			result, err := app.api.Exec(cmd.Context(), id, command, opts)
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
	addSpecFileFlag(cmd, &specPath, "read the command and its options from a YAML or JSON file (flags override it)")
	cmd.Flags().StringArrayVarP(
		&flags.env,
		"env",
		"e",
		nil,
		"set an environment variable for this command only (KEY=VALUE); repeat for multiple values",
	)
	cmd.Flags().StringVarP(
		&flags.workDir,
		"workdir",
		"w",
		"",
		"working directory for this command (default: the image's own)",
	)
	cmd.Flags().DurationVar(
		&flags.timeout,
		"timeout",
		runtime.DefaultExecTimeout,
		"how long the command may run before it is cancelled (e.g. 5s, 2m)",
	)

	return cmd
}
