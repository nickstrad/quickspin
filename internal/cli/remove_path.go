package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

func (app *application) newRemovePathCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rm ID PATH",
		Short: "Remove a path inside a sandbox",
		Example: `  ID=$(quickspin sandbox create alpine:3.20 -o json | jq -r .sandbox_id)

  quickspin sandbox rm "$ID" /app/config.yaml
  quickspin sandbox rm "$ID" /app`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, filePath := args[0], args[1]
			app.logCommand(cmd, "sandboxID", id, "path", filePath)

			if err := app.api.RemovePath(cmd.Context(), id, filePath); err != nil {
				return fmt.Errorf("remove path %s from sandbox %q: %w", filePath, id, err)
			}
			return nil
		},
	}
}
