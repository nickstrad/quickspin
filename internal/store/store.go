package store

import (
	"context"
	"encoding/json"
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

func canTransition(from, to TaskState) error {
	if !from.valid() || !to.valid() {
		return ErrInvalidState
	}
	if !slices.Contains(validTransitions[from], to) {
		return ErrInvalidStateTransition
	}
	return nil
}

type Store interface {
	GetIdempotencyKey(ctx context.Context, idempotencyKey string) (*IdempotencyKey, error)
	CreateIdempotencyKey(ctx context.Context, idempotencyKey, sandboxID string) (*IdempotencyKey, error)
	CreateSandbox(ctx context.Context, idempotencyKey string, spec SpecFile) (*Sandbox, error)
	GetSandbox(ctx context.Context, sandboxID string) (*Sandbox, error)
	GetSandboxes(ctx context.Context) ([]*Sandbox, error)
	UpdateSandboxState(ctx context.Context, sandboxID string, from, to TaskState) (*Sandbox, error)
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
		return "", E("store.SpecFile.ToJSON", "serializing spec", ErrInvalidSpec)
	}

	data, err := json.Marshal(s)
	if err != nil {
		return "", E("store.SpecFile.ToJSON", "marshaling spec to json", err)
	}

	return string(data), nil
}

// An empty SpecFile represents a request that uses every default.
func (s *SpecFile) Validate() error {
	if s == nil {
		return E("store.SpecFile.Validate", "spec is nil", ErrInvalidSpec)
	}
	return nil
}

type Sandbox struct {
	ID        int       `json:"-" yaml:"-"`
	SandboxID string    `json:"sandbox_id" yaml:"sandbox_id"`
	State     TaskState `json:"state" yaml:"state"`
	Spec      SpecFile  `json:"spec" yaml:"spec"`
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
