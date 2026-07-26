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
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			infos, err := app.runtime.List(cmd.Context())
			if err != nil {
				return fmt.Errorf("list sandboxes: %w", err)
			}

			return app.renderer.writeInfos(cmd.OutOrStdout(), sortedInfos(infos))
		},
	}
}

func sortedInfos(infos []runtime.Info) []runtime.Info {
	sorted := slices.Clone(infos)
	slices.SortFunc(sorted, func(a, b runtime.Info) int {
		if created := a.CreatedAt.Compare(b.CreatedAt); created != 0 {
			return created
		}
		return strings.Compare(a.ID, b.ID)
	})
	if sorted == nil {
		return []runtime.Info{}
	}
	return sorted
}
