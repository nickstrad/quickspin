// Package sandbox is the domain: what a sandbox IS. The sandbox record, the
// task state machine, and the spec document with its defaults and resolution
// live here so that callers can reason about a sandbox without importing a
// storage package.
package sandbox

import (
	"encoding/json"
	"fmt"
	"slices"
	"time"
)

type TaskState string

const (
	Pending  TaskState = "pending"
	Running  TaskState = "running"
	Failed   TaskState = "failed"
	Stopping TaskState = "stopping"
	Stopped  TaskState = "stopped"
)

// Every task state appears as a key, so key membership doubles as state validity.
var validTransitions = map[TaskState][]TaskState{
	Pending:  {Running, Failed},
	Running:  {Failed, Stopping},
	Failed:   {},
	Stopping: {Stopped},
	Stopped:  {},
}

func (s TaskState) valid() bool {
	_, ok := validTransitions[s]
	return ok
}

func CanTransition(from, to TaskState) error {
	if !from.valid() || !to.valid() {
		return ErrInvalidState
	}
	if !slices.Contains(validTransitions[from], to) {
		return ErrInvalidStateTransition
	}
	return nil
}

// SpecFile preserves the difference between absent and explicit zero values.
type SpecFile struct {
	Image        *string           `json:"image" yaml:"image"`
	Env          map[string]string `json:"env" yaml:"env"`
	CPUs         *float64          `json:"cpus" yaml:"cpus"`
	Memory       *string           `json:"memory" yaml:"memory"`
	PidsLimit    *int64            `json:"pids-limit" yaml:"pids-limit"`
	AllowNetwork *bool             `json:"allow-network" yaml:"allow-network"`
}

func (s *SpecFile) ToJSON() (string, error) {
	if s == nil {
		return "", E("sandbox.SpecFile.ToJSON", "serializing spec", ErrInvalidSpec)
	}

	data, err := json.Marshal(s)
	if err != nil {
		return "", E("sandbox.SpecFile.ToJSON", "marshaling spec to json", err)
	}

	return string(data), nil
}

// An empty SpecFile represents a request that uses every default.
func (s *SpecFile) Validate() error {
	if s == nil {
		return E("sandbox.SpecFile.Validate", "spec is nil", ErrInvalidSpec)
	}
	return nil
}

// An omitted TTL means DefaultTTL, never "forever".
const (
	DefaultTTL = 15 * time.Minute
	MaxTTL     = 24 * time.Hour
)

// ResolveTTL turns a requested lifetime into the one that will be enforced.
func ResolveTTL(requested time.Duration) (time.Duration, error) {
	const op = "sandbox.ResolveTTL"

	if requested == 0 {
		return DefaultTTL, nil
	}
	if requested < 0 || requested > MaxTTL {
		return 0, E(op, fmt.Sprintf("ttl %s is outside (0, %s]", requested, MaxTTL), ErrInvalidSpec)
	}
	return requested, nil
}

type Sandbox struct {
	ID        int       `json:"-" yaml:"-"`
	SandboxID string    `json:"sandbox_id" yaml:"sandbox_id"`
	State     TaskState `json:"state" yaml:"state"`
	Spec      SpecFile  `json:"spec" yaml:"spec"`
	ExpiresAt time.Time `json:"expires_at" yaml:"expires_at"`
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`
	UpdatedAt time.Time `json:"updated_at" yaml:"updated_at"`
}

type IdempotencyKey struct {
	ID        int       `json:"-" yaml:"-"`
	SandboxID string    `json:"sandbox_id" yaml:"sandbox_id"`
	Key       string    `json:"key" yaml:"key"`
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`
	UpdatedAt time.Time `json:"updated_at" yaml:"updated_at"`
}
