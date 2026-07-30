package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/spf13/cobra"
)

func (app *application) newListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List sandboxes",
		Example: `  quickspin sandbox list
  quickspin sandbox list -o yaml

  # Every ID, one per line.
  quickspin sandbox list -o json | jq -r '.[].id'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app.logCommand(cmd)

			infos, err := app.runtime.List(cmd.Context())
			if err != nil {
				return fmt.Errorf("list sandboxes: %w", err)
			}

			return app.renderer.writeInfos(cmd.OutOrStdout(), sortedInfos(infos))
		},
	}
}

func sortedInfos(infos []runtime.Info) []runtime.Info {
	return sortedCopy(infos, func(a, b runtime.Info) int {
		if created := a.CreatedAt.Compare(b.CreatedAt); created != 0 {
			return created
		}
		return strings.Compare(a.ID, b.ID)
	})
}

// Returns an empty (never nil) slice so JSON output renders [] instead of null,
// and never sorts the runtime-owned slice in place.
func sortedCopy[T any](items []T, cmp func(a, b T) int) []T {
	sorted := slices.Clone(items)
	slices.SortFunc(sorted, cmp)
	if sorted == nil {
		return []T{}
	}
	return sorted
}
