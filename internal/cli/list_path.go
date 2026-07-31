package cli

import (
	"fmt"
	"strings"

	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/spf13/cobra"
)

func (app *application) newListPathCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ls ID PATH",
		Short: "List a path inside a sandbox",
		Example: `  ID=$(quickspin sandbox create alpine:3.20 -o json | jq -r .sandbox_id)

  quickspin sandbox ls "$ID" /app

  # A single file path lists just that file — how to check the mode a cp landed with.
  quickspin sandbox ls "$ID" /app/config.yaml`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, filePath := args[0], args[1]
			app.logCommand(cmd, "sandboxID", id, "path", filePath)

			infos, err := app.api.ListDir(cmd.Context(), id, filePath)
			if err != nil {
				return fmt.Errorf("list path %s in sandbox %q: %w", filePath, id, err)
			}
			sorted := sortedCopy(infos, func(a, b runtime.FileInfo) int {
				return strings.Compare(a.Path, b.Path)
			})
			return app.renderer.writeFileInfos(cmd.OutOrStdout(), sorted)
		},
	}
}
