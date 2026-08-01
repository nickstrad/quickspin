package store

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/nickstrad/quickspin/internal/runtime"
)

// Defaults keep omitted limits enforceable rather than treating them as unlimited.
const (
	DefaultCPUs      = 1.0
	DefaultMemory    = "512m"
	DefaultPidsLimit = 256
	DefaultImage     = "alpine:3.20"
)

// Resolve applies defaults and converts Memory to bytes without validating limits.
func (s *SpecFile) Resolve() (runtime.Spec, error) {
	const op = "store.SpecFile.Resolve"

	if s == nil {
		return runtime.Spec{}, E(op, "spec is nil", ErrInvalidSpec)
	}

	memoryBytes, err := parseMemory(orDefault(s.Memory, DefaultMemory))
	if err != nil {
		return runtime.Spec{}, E(op, err.Error(), ErrInvalidSpec)
	}

	return runtime.NewSpec(
		orDefault(s.Image, DefaultImage),
		s.Env,
		orDefault(s.CPUs, DefaultCPUs),
		memoryBytes,
		orDefault(s.PidsLimit, DefaultPidsLimit),
		orDefault(s.AllowNetwork, false),
	), nil
}

func orDefault[T any](value *T, fallback T) T {
	if value != nil {
		return *value
	}
	return fallback
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
