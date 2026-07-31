package cli

import (
	"fmt"
	"slices"
	"strings"

	"github.com/nickstrad/quickspin/internal/store"
	"github.com/spf13/cobra"
)

func (app *application) newListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List sandboxes",
		Example: `  quickspin sandbox list
  quickspin sandbox list -o yaml

  # Every ID, one per line.
  quickspin sandbox list -o json | jq -r '.[].sandbox_id'`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app.logCommand(cmd)

			sandboxes, err := app.api.ListSandboxes(cmd.Context())
			if err != nil {
				return fmt.Errorf("list sandboxes: %w", err)
			}

			return app.renderer.writeSandboxes(cmd.OutOrStdout(), sortedSandboxes(sandboxes))
		},
	}
}

func sortedSandboxes(sandboxes []*store.Sandbox) []*store.Sandbox {
	return sortedCopy(sandboxes, func(a, b *store.Sandbox) int {
		if created := a.CreatedAt.Compare(b.CreatedAt); created != 0 {
			return created
		}
		return strings.Compare(a.SandboxID, b.SandboxID)
	})
}

func sortedCopy[T any](items []T, cmp func(a, b T) int) []T {
	sorted := append([]T{}, items...)
	slices.SortFunc(sorted, cmp)
	return sorted
}
