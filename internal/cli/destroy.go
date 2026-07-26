package cli

import (
	"fmt"

	"github.com/spf13/cobra"
)

type destroyResult struct {
	ID     string `json:"id" yaml:"id"`
	Status string `json:"status" yaml:"status"`
}

func (app *application) newDestroyCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "destroy ID",
		Short: "Destroy a sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := app.runtime.Destroy(cmd.Context(), args[0]); err != nil {
				return fmt.Errorf("destroy sandbox %q: %w", args[0], err)
			}

			return app.renderer.writeDestroyResult(cmd.OutOrStdout(), destroyResult{
				ID:     args[0],
				Status: "destroyed",
			})
		},
	}
}
