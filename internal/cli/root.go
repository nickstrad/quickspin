package cli

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/spf13/cobra"
)

type application struct {
	runtime  runtime.Runtime
	renderer renderer
	logger   *slog.Logger
}

// NewCommand builds the CLI around the backend-neutral runtime contract.
// logLevel must also back the supplied logger's handler so the persistent flag
// can change every child logger that shares that handler.
func NewCommand(rt runtime.Runtime, logger *slog.Logger, logLevel *slog.LevelVar) *cobra.Command {
	if logger == nil {
		panic("cli.NewCommand: logger is required")
	}
	if logLevel == nil {
		panic("cli.NewCommand: log level is required")
	}

	app := &application{
		runtime: rt,
		renderer: renderer{
			format: outputTable,
		},
		logger: logger,
	}

	cmd := &cobra.Command{
		Use:   "quickspin",
		Short: "Create and manage Quickspin sandboxes",
		Example: `  # A whole session: create, use, destroy.
  ID=$(quickspin sandbox create alpine:3.20 -o json | jq -r .id)
  quickspin sandbox exec "$ID" -- sh -c 'echo hello'
  quickspin sandbox destroy "$ID"

  # -o yaml and -o json work on every command that prints a record.
  quickspin sandbox list -o yaml

  # Anything unexpected: ask the CLI what it is doing.
  quickspin --log-level debug sandbox list`,
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.NoArgs,
		RunE:          showHelp,
	}
	cmd.PersistentFlags().Var(
		logLevelFlag{level: logLevel},
		"log-level",
		"log level: debug, info, warn, or error",
	)
	cmd.PersistentFlags().VarP(
		&app.renderer.format,
		"output",
		"o",
		"output format: table, json, or yaml",
	)
	cmd.RegisterFlagCompletionFunc("output", completeOutputFormat)
	cmd.AddCommand(app.newSandboxCommand())

	return cmd
}

type logLevelFlag struct {
	level *slog.LevelVar
}

func (f logLevelFlag) Set(value string) error {
	var level slog.Level
	switch strings.ToLower(value) {
	case "debug":
		level = slog.LevelDebug
	case "info":
		level = slog.LevelInfo
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return fmt.Errorf("invalid log level %q: expected debug, info, warn, or error", value)
	}
	f.level.Set(level)
	return nil
}

func (f logLevelFlag) String() string {
	return f.level.Level().String()
}

func (logLevelFlag) Type() string {
	return "level"
}

func (app *application) logCommand(cmd *cobra.Command, attrs ...any) {
	app.logger.DebugContext(cmd.Context(), "executing "+cmd.Name()+" command", attrs...)
}

func showHelp(cmd *cobra.Command, _ []string) error {
	return cmd.Help()
}
