package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func (app *application) newInspectCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "inspect ID",
		Short: "Inspect a sandbox",
		Example: `  ID=$(quickspin sandbox create alpine:3.20 -o json | jq -r .id)

  quickspin sandbox inspect "$ID"
  quickspin sandbox inspect "$ID" -o yaml`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app.logCommand(cmd, "sandboxID", args[0])

			info, err := app.runtime.Inspect(cmd.Context(), args[0])
			if err != nil {
				return fmt.Errorf("inspect sandbox %q: %w", args[0], err)
			}

			return app.renderer.writeInfo(cmd.OutOrStdout(), info)
		},
	}
}
