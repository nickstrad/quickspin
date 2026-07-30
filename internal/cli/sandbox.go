package cli

import "github.com/spf13/cobra"

func (app *application) newSandboxCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Manage sandboxes",
		Args:  cobra.NoArgs,
		RunE:  showHelp,
	}
	cmd.AddCommand(
		app.newCreateCommand(),
		app.newListCommand(),
		app.newInspectCommand(),
		app.newExecCommand(),
		app.newCopyCommand(),
		app.newListPathCommand(),
		app.newRemovePathCommand(),
		app.newDestroyCommand(),
	)

	return cmd
}
