package cli

import (
	"fmt"
	"time"

	"github.com/nickstrad/quickspin/internal/sandbox"
	"github.com/spf13/cobra"
)

func (app *application) newKeepaliveCommand() *cobra.Command {
	var ttl time.Duration

	cmd := &cobra.Command{
		Use:   "keepalive ID",
		Short: "Renew a sandbox's lifetime",
		Long: "Renew a pending or running sandbox's lifetime from now. " +
			"An omitted --ttl uses the server default, and durations above the maximum are clamped by the server.",
		Example: `  ID=$(quickspin sandbox create alpine:3.20 -o json | jq -r .sandbox_id)

  quickspin sandbox keepalive "$ID"
  quickspin sandbox keepalive "$ID" --ttl 30m -o json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			sandboxID := args[0]
			app.logCommand(cmd, "sandboxID", sandboxID, "ttl", ttl)

			sbx, err := app.api.KeepaliveSandbox(cmd.Context(), sandboxID, ttl)
			if err != nil {
				return fmt.Errorf("keep sandbox %q alive: %w", sandboxID, err)
			}

			return app.renderer.writeSandbox(cmd.OutOrStdout(), sbx)
		},
	}
	cmd.Flags().DurationVar(
		&ttl,
		"ttl",
		0,
		fmt.Sprintf("renew the sandbox for this long from now (default %s, max %s)",
			sandbox.DefaultTTL, sandbox.MaxTTL),
	)

	return cmd
}
