package cli

import (
	"fmt"

	"github.com/nickstrad/quickspin/internal/httpapi"
	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/nickstrad/quickspin/internal/store"
	"github.com/spf13/cobra"
)

func (app *application) newServeCommand() *cobra.Command {
	var (
		host   string
		port   int
		dbPath string
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
			ctx := cmd.Context()
			app.logCommand(cmd, "host", host, "port", port, "db", dbPath)

			sandboxRuntime, err := runtime.NewDockerRuntime(nil, app.logger.With(
				"subcomponent", "runtime",
				"backend", "docker",
			))
			if err != nil {
				return fmt.Errorf("open the docker runtime: %w", err)
			}

			sandboxStore, err := store.NewSqlliteStore(ctx, dbPath, "", app.logger.With("subcomponent", "store"))
			if err != nil {
				return fmt.Errorf("open the store: %w", err)
			}
			defer sandboxStore.Cleanup()

			server := httpapi.NewAPI(host, port, app.logger, sandboxStore, sandboxRuntime)
			app.logger.InfoContext(ctx, "control plane listening", "host", host, "port", port)

			server.Start(ctx.Done())
			return nil
		},
	}
	cmd.Flags().StringVar(&host, "host", "127.0.0.1", "address to listen on")
	cmd.Flags().IntVar(&port, "port", 8080, "port to listen on")
	cmd.Flags().StringVar(&dbPath, "db", "", "path to the SQLite database file")
	cmd.MarkFlagFilename("db")

	return cmd
}
