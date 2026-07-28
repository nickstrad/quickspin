package cli

import (
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

func (app *application) newCreateCommand() *cobra.Command {
	var (
		env          []string
		cpus         float64
		memory       string
		pidsLimit    int64
		allowNetwork bool
	)

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

			memoryBytes, err := parseMemory(memory)
			if err != nil {
				return err
			}

			spec := runtime.NewSpec(args[0], environment, cpus, memoryBytes, pidsLimit, allowNetwork)
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
	cmd.Flags().StringArrayVarP(
		&env,
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
		&cpus,
		"cpus",
		defaultCPUs,
		"CPU cores the sandbox may use (fractional allowed, e.g. 0.5)",
	)
	cmd.Flags().StringVarP(
		&memory,
		"memory",
		"m",
		defaultMemory,
		"memory limit, with an optional b/k/m/g suffix (e.g. 512m)",
	)
	cmd.Flags().Int64Var(
		&pidsLimit,
		"pids-limit",
		defaultPidsLimit,
		"maximum number of processes the sandbox may have",
	)
	cmd.Flags().BoolVar(
		&allowNetwork,
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
