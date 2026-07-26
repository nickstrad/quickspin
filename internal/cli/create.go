package cli

import (
	"fmt"
	"strings"

	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/spf13/cobra"
)

func (app *application) newCreateCommand() *cobra.Command {
	var env []string

	cmd := &cobra.Command{
		Use:   "create IMAGE",
		Short: "Create a sandbox",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			app.logCommand(cmd, "image", args[0])

			environment, err := parseEnvironment(env)
			if err != nil {
				return err
			}

			info, err := app.runtime.Create(cmd.Context(), runtime.Spec{
				Image: args[0],
				Env:   environment,
			})
			if err != nil {
				return fmt.Errorf("create sandbox: %w", err)
			}

			return app.renderer.writeInfo(cmd.OutOrStdout(), info)
		},
	}
	cmd.Flags().StringArrayVarP(
		&env,
		"env",
		"e",
		nil,
		"set an environment variable (KEY=VALUE); repeat for multiple values",
	)

	return cmd
}

func parseEnvironment(values []string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}

	env := make(map[string]string, len(values))
	for _, value := range values {
		key, val, ok := strings.Cut(value, "=")
		if !ok || key == "" {
			return nil, fmt.Errorf("invalid environment variable %q: expected KEY=VALUE", value)
		}
		env[key] = val
	}

	return env, nil
}
