package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func (app *application) newEventsCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "events ID",
		Short: "Show a sandbox's lifecycle events",
		Example: `  ID=$(quickspin sandbox create alpine:3.20 -o json | jq -r .sandbox_id)

  quickspin sandbox events "$ID"
  quickspin sandbox events "$ID" -o json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxID := args[0]
			app.logCommand(cmd, "sandboxID", sandboxID)

			evts, err := app.api.GetSandboxEvents(cmd.Context(), sandboxID)
			if err != nil {
				return fmt.Errorf("list events for sandbox %q: %w", sandboxID, err)
			}

			return app.renderer.writeEvents(cmd.OutOrStdout(), evts)
		},
	}
}
