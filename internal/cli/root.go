package cli

import (
	"context"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/nickstrad/quickspin/internal/client"
	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/nickstrad/quickspin/internal/sandbox"
	"github.com/spf13/cobra"
)

type sandboxAPI interface {
	CreateSandbox(ctx context.Context, idempotencyKey string, spec sandbox.SpecFile, ttl time.Duration) (*sandbox.Sandbox, error)
	ListSandboxes(ctx context.Context) ([]*sandbox.Sandbox, error)
	InspectSandbox(ctx context.Context, sandboxID string) (runtime.Info, error)
	DestroySandbox(ctx context.Context, sandboxID string) error
	Exec(ctx context.Context, sandboxID string, cmd []string, opts runtime.ExecOpts) (runtime.ExecResult, error)
	WriteFile(ctx context.Context, sandboxID, path string, content []byte, mode fs.FileMode) error
	ReadFile(ctx context.Context, sandboxID, path string) ([]byte, error)
	ListDir(ctx context.Context, sandboxID, path string) ([]runtime.FileInfo, error)
	RemovePath(ctx context.Context, sandboxID, path string) error
}

type application struct {
	api      sandboxAPI
	renderer renderer
	logger   *slog.Logger
}

const serverAddressEnv = "QUICKSPIN_SERVER"

// NewCommand builds the CLI. A nil api uses the server selected by --server.
// logLevel must also back the supplied logger's handler so the persistent flag
// can change every child logger that shares that handler.
func NewCommand(api sandboxAPI, logger *slog.Logger, logLevel *slog.LevelVar) *cobra.Command {
	if logger == nil {
		panic("cli.NewCommand: logger is required")
	}
	if logLevel == nil {
		panic("cli.NewCommand: log level is required")
	}

	app := &application{
		api:      api,
		renderer: renderer{format: outputTable},
		logger:   logger,
	}

	serverURL := os.Getenv(serverAddressEnv)
	if serverURL == "" {
		serverURL = client.DefaultBaseURL
	}

	cmd := &cobra.Command{
		Use:   "quickspin",
		Short: "Create and manage Quickspin sandboxes",
		Example: `  # The control plane first; every other command is a client of it.
  quickspin serve &

  # A whole session: create, use, destroy.
  ID=$(quickspin sandbox create alpine:3.20 -o json | jq -r .sandbox_id)
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
		PersistentPreRunE: func(_ *cobra.Command, _ []string) error {
			if app.api == nil {
				app.api = client.New(serverURL, nil)
			}
			return nil
		},
	}
	cmd.PersistentFlags().StringVar(
		&serverURL,
		"server",
		serverURL,
		"control plane base URL (env "+serverAddressEnv+")",
	)
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
	cmd.AddCommand(app.newSandboxCommand(), app.newServeCommand())

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
