package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/nickstrad/quickspin/internal/store"
	"github.com/spf13/cobra"
)

type createFlags struct {
	env          []string
	cpus         float64
	memory       string
	pidsLimit    int64
	allowNetwork bool
}

func resolveCreateSpec(
	args []string,
	file store.SpecFile,
	flags createFlags,
	flagSet func(name string) bool,
) (store.SpecFile, error) {
	if len(args) > 0 && args[0] != "" {
		file.Image = &args[0]
	}
	if file.Image == nil || *file.Image == "" {
		return store.SpecFile{}, errors.New("no image: pass IMAGE as an argument or set image in the spec file")
	}

	environment, err := resolveEnvironment(file.Env, flags.env)
	if err != nil {
		return store.SpecFile{}, err
	}
	file.Env = environment

	if flagSet("cpus") {
		file.CPUs = &flags.cpus
	}
	if flagSet("memory") {
		file.Memory = &flags.memory
	}
	if flagSet("pids-limit") {
		file.PidsLimit = &flags.pidsLimit
	}
	if flagSet("allow-network") {
		file.AllowNetwork = &flags.allowNetwork
	}

	// Validate the resolved values while keeping defaults out of the wire form.
	resolved, err := file.Resolve()
	if err != nil {
		return store.SpecFile{}, err
	}
	if err := resolved.Validate(); err != nil {
		return store.SpecFile{}, fmt.Errorf("invalid limits: %w", err)
	}

	return file, nil
}

func (app *application) newCreateCommand() *cobra.Command {
	var (
		specPath string
		flags    createFlags
	)

	cmd := &cobra.Command{
		Use:   "create [IMAGE]",
		Short: "Create a sandbox",
		Long: "Create a sandbox.\n\n" +
			"Inputs may come from flags, from a --file spec, or from both. The file may\n" +
			"be YAML or JSON; its keys match the flag names. Flags win over the file,\n" +
			"and --env merges into the file's env one variable at a time.",
		Example: `  # Defaults: 1 CPU, 512m memory, 256 pids, no network.
  quickspin sandbox create alpine:3.20

  # The same request as a spec file. Keys match the flag names, and the file
  # may be YAML or JSON. Runnable as written with a bash/zsh heredoc; the EOF
  # terminator must sit at the start of its own line.
  quickspin sandbox create -f <(cat <<'EOF'
image: alpine:3.20
cpus: 0.5
memory: 256m
pids-limit: 64
allow-network: true
env:
  GREETING: hello
  NAME: world
EOF
  )

  # Flags beat the file, so one spec covers many runs.
  quickspin sandbox create -f sandbox.yaml --memory 1g -e GREETING=hi

  # Keep the ID for the commands that follow.
  ID=$(quickspin sandbox create alpine:3.20 -o json | jq -r .sandbox_id)`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			file, err := loadFile[store.SpecFile](specPath)
			if err != nil {
				return err
			}

			spec, err := resolveCreateSpec(args, file, flags, cmd.Flags().Changed)
			if err != nil {
				return err
			}
			app.logCommand(cmd, "image", *spec.Image, "specFile", specPath)

			sandbox, err := app.api.CreateSandbox(cmd.Context(), uuid.NewString(), spec)
			if err != nil {
				return fmt.Errorf("create sandbox: %w", err)
			}

			return app.renderer.writeSandbox(cmd.OutOrStdout(), sandbox)
		},
	}
	addSpecFileFlag(cmd, &specPath, "read the sandbox spec from a YAML or JSON file (flags override it)")
	cmd.Flags().StringArrayVarP(
		&flags.env,
		"env",
		"e",
		nil,
		"set an environment variable (KEY=VALUE); repeat for multiple values",
	)
	cmd.Flags().Float64Var(
		&flags.cpus,
		"cpus",
		store.DefaultCPUs,
		"CPU cores the sandbox may use (fractional allowed, e.g. 0.5)",
	)
	cmd.Flags().StringVarP(
		&flags.memory,
		"memory",
		"m",
		store.DefaultMemory,
		"memory limit, with an optional b/k/m/g suffix (e.g. 512m)",
	)
	cmd.Flags().Int64Var(
		&flags.pidsLimit,
		"pids-limit",
		store.DefaultPidsLimit,
		"maximum number of processes the sandbox may have",
	)
	cmd.Flags().BoolVar(
		&flags.allowNetwork,
		"allow-network",
		false,
		"give the sandbox network access (default: no network)",
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
