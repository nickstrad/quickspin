package cli

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/spf13/cobra"
)

// Flag defaults. Every sandbox gets a limit whether or not the caller names one,
// because runtime.Spec rejects a zero limit rather than treating it as unlimited
// — there is no flag value that means "no ceiling".
const (
	defaultCPUs      = 1.0
	defaultMemory    = "512m"
	defaultPidsLimit = 256
)

// createFlags collects what the create flags carry, so resolveCreateSpec can be
// tested without building a cobra command.
type createFlags struct {
	env          []string
	cpus         float64
	memory       string
	pidsLimit    int64
	allowNetwork bool
}

// args holds the IMAGE argument, if the caller gave one. An empty argument list
// is allowed only because the spec file can name the image instead.
func resolveCreateSpec(
	args []string,
	file specFile,
	flags createFlags,
	flagSet func(name string) bool,
) (runtime.Spec, error) {
	var argImage string
	if len(args) == 1 {
		argImage = args[0]
	}
	image := resolve("", file.Image, argImage != "", argImage)
	if image == "" {
		return runtime.Spec{}, errors.New("no image: pass IMAGE as an argument or set image in the spec file")
	}

	environment, err := resolveEnvironment(file.Env, flags.env)
	if err != nil {
		return runtime.Spec{}, err
	}

	memory := resolve(defaultMemory, file.Memory, flagSet("memory"), flags.memory)
	memoryBytes, err := parseMemory(memory)
	if err != nil {
		return runtime.Spec{}, err
	}

	return runtime.NewSpec(
		image,
		environment,
		resolve(defaultCPUs, file.CPUs, flagSet("cpus"), flags.cpus),
		memoryBytes,
		resolve(defaultPidsLimit, file.PidsLimit, flagSet("pids-limit"), flags.pidsLimit),
		resolve(false, file.AllowNetwork, flagSet("allow-network"), flags.allowNetwork),
	), nil
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
  ID=$(quickspin sandbox create alpine:3.20 -o json | jq -r .id)`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			file, err := loadFile[specFile](specPath)
			if err != nil {
				return err
			}

			spec, err := resolveCreateSpec(args, file, flags, cmd.Flags().Changed)
			if err != nil {
				return err
			}
			app.logCommand(cmd, "image", spec.Image, "specFile", specPath)

			// Validating here rather than letting Create do it turns a bad flag into
			// a usage error before any daemon work starts. Create validates again;
			// it cannot trust that every caller is this command.
			if err := spec.Validate(); err != nil {
				return fmt.Errorf("invalid limits: %w", err)
			}

			info, err := app.runtime.Create(cmd.Context(), spec)
			if err != nil {
				return fmt.Errorf("create sandbox: %w", err)
			}

			return app.renderer.writeInfo(cmd.OutOrStdout(), info)
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
	// These three are not Docker features; they are a translation onto cgroup v2
	// controller files the kernel enforces — cpu.max, memory.max, and pids.max in
	// the container's cgroup. The flag names match Docker's so what is learned
	// here transfers, but the enforcement is the kernel's either way.
	cmd.Flags().Float64Var(
		&flags.cpus,
		"cpus",
		defaultCPUs,
		"CPU cores the sandbox may use (fractional allowed, e.g. 0.5)",
	)
	cmd.Flags().StringVarP(
		&flags.memory,
		"memory",
		"m",
		defaultMemory,
		"memory limit, with an optional b/k/m/g suffix (e.g. 512m)",
	)
	cmd.Flags().Int64Var(
		&flags.pidsLimit,
		"pids-limit",
		defaultPidsLimit,
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

// memoryUnits are binary multiples, matching Docker: "1m" is 1 MiB, not
// 1,000,000 bytes. A bare number is bytes.
var memoryUnits = map[byte]int64{
	'b': 1,
	'k': 1 << 10,
	'm': 1 << 20,
	'g': 1 << 30,
}

func parseMemory(value string) (int64, error) {
	trimmed := strings.TrimSpace(strings.ToLower(value))
	if trimmed == "" {
		return 0, fmt.Errorf("invalid memory limit %q: expected a size like 512m", value)
	}

	multiplier := int64(1)
	if unit, ok := memoryUnits[trimmed[len(trimmed)-1]]; ok {
		multiplier = unit
		trimmed = trimmed[:len(trimmed)-1]
	}

	// Integers only: a "1.5g" that silently truncated would set a limit the caller
	// did not ask for, and the units matter too much for that to be quiet.
	digits, err := strconv.ParseInt(trimmed, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid memory limit %q: expected a whole number with an optional b/k/m/g suffix", value)
	}
	if digits > (1<<62)/multiplier {
		return 0, fmt.Errorf("invalid memory limit %q: too large", value)
	}

	return digits * multiplier, nil
}
