package cli

import "github.com/spf13/cobra"

func (app *application) newSandboxCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Manage sandbox lifecycles",
		Args:  cobra.NoArgs,
		RunE:  showHelp,
	}
	cmd.AddCommand(
		app.newCreateCommand(),
		app.newListCommand(),
		app.newInspectCommand(),
		app.newExecCommand(),
		app.newDestroyCommand(),
	)

	return cmd
}
