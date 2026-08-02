package cli

import (
	"time"

	"github.com/nickstrad/quickspin/internal/daemon"
	"github.com/spf13/cobra"
)

func (app *application) newServeCommand() *cobra.Command {
	var (
		host              string
		port              int
		dbPath            string
		reconcileInterval time.Duration
	)

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Run the control plane",
		Long: "Run the control plane.\n\n" +
			"The server owns the sandbox records and the runtime; the other commands\n" +
			"speak to it over the JSON API. Point them at it with --server or " +
			serverAddressEnv + ".",
		Example: `  quickspin serve
  quickspin serve --port 9000 --db /var/lib/quickspin/quickspin.db

  # In another shell.
  quickspin --server http://127.0.0.1:9000 sandbox list`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app.logCommand(cmd, "host", host, "port", port, "db", dbPath,
				"reconcileInterval", reconcileInterval)

			return daemon.Serve(cmd.Context(), daemon.Config{
				Host:               host,
				Port:               port,
				DBPath:             dbPath,
				Logger:             app.logger,
				ReconcilerInterval: reconcileInterval,
			})
		},
	}
	cmd.Flags().StringVar(&host, "host", "127.0.0.1", "address to listen on")
	cmd.Flags().IntVar(&port, "port", 8080, "port to listen on")
	cmd.Flags().StringVar(&dbPath, "db", "", "path to the SQLite database file")
	cmd.Flags().DurationVar(
		&reconcileInterval,
		"reconcile-interval",
		0,
		"how often the reconciler sweeps sandboxes (default: the daemon's own 15s)",
	)
	cmd.MarkFlagFilename("db")

	return cmd
}
