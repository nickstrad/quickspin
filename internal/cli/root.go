package cli

import (
	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/spf13/cobra"
)

type application struct {
	runtime  runtime.Runtime
	renderer renderer
}

// NewCommand builds the CLI around the backend-neutral runtime contract.
func NewCommand(rt runtime.Runtime) *cobra.Command {
	app := &application{
		runtime: rt,
		renderer: renderer{
			format: outputTable,
		},
	}

	cmd := &cobra.Command{
		Use:           "quickspin",
		Short:         "Create and manage Quickspin sandboxes",
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.NoArgs,
		RunE:          showHelp,
	}
	cmd.PersistentFlags().VarP(
		&app.renderer.format,
		"output",
		"o",
		"output format: table, json, or yaml",
	)
	cmd.RegisterFlagCompletionFunc("output", completeOutputFormat)
	cmd.AddCommand(app.newSandboxCommand())

	return cmd
}

func showHelp(cmd *cobra.Command, _ []string) error {
	return cmd.Help()
}
