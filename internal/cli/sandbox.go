package cli

import "github.com/spf13/cobra"

func (app *application) newSandboxCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sandbox",
		Short: "Manage sandboxes",
		Example: `  ID=$(quickspin sandbox create alpine:3.20 -o json | jq -r .sandbox_id)

  # Two argument forms to know: KEY=VALUE for --env, and ID:/absolute/path for cp.
  quickspin sandbox create alpine:3.20 -e NAME=world
  quickspin sandbox cp ./config.yaml "$ID":/app/config.yaml

  # See "quickspin sandbox cp --help" for writing a file straight from the shell.`,
		Args: cobra.NoArgs,
		RunE: showHelp,
	}
	cmd.AddCommand(
		app.newCreateCommand(),
		app.newListCommand(),
		app.newInspectCommand(),
		app.newEventsCommand(),
		app.newKeepaliveCommand(),
		app.newExecCommand(),
		app.newCopyCommand(),
		app.newListPathCommand(),
		app.newRemovePathCommand(),
		app.newDestroyCommand(),
	)

	return cmd
}
