package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
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

func isValidTaskState(s string) bool {
	_, ok := validTransitions[TaskState(s)]
	return ok
}

func canTransition(from, to string) error {
	if !isValidTaskState(from) || !isValidTaskState(to) {
		return ErrInvalidState
	}
	if !slices.Contains(validTransitions[TaskState(from)], TaskState(to)) {
		return ErrInvalidStateTransition
	}
	return nil
}

type Store interface {
	GetIdempotencyKey(ctx context.Context, idempotencyKey string) (*IdempotencyKey, error)
	CreateIdempotencyKey(ctx context.Context, idempotencyKey string, sandboxID int) (*IdempotencyKey, error)
	UpdateSandboxState(ctx context.Context, from string, to string, sandboxID string) (*Sandbox, error)
	CreateSandbox(ctx context.Context, idempotencyKey string, spec string) (*Sandbox, error)
	GetSandbox(ctx context.Context, sandboxID string) (*Sandbox, error)
}

// specFile is the file form of a create request. Every field is a pointer (or a
// nil-able map) so an absent key and an explicit zero stay distinguishable:
// `cpus: 0` has to reach Validate and be rejected, exactly as `--cpus 0` is,
// rather than quietly picking up the default.
//
// Keys match the create flag names so there is one spelling to learn.
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

	bytes, err := json.Marshal(s)
	if err != nil {
		return "", E("store.SpecFile.ToJSON", "marshaling spec to json", err)
	}

	return string(bytes), nil
}

var specParsers = []struct {
	name      string
	unmarshal func([]byte, any) error
}{
	{"JSON", json.Unmarshal},
	{"YAML", yaml.Unmarshal},
}

func parseSpecFile(s string) (*SpecFile, error) {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return nil, E("store.parseSpecFile", "spec is empty", ErrInvalidSpec)
	}

	data := []byte(trimmed)
	var parseErrs []error
	for _, p := range specParsers {
		spec := &SpecFile{}
		if err := p.unmarshal(data, spec); err != nil {
			parseErrs = append(parseErrs, fmt.Errorf("%s error: %v", p.name, err))
			continue
		}
		if !isSpecPopulated(spec) {
			return nil, E("store.parseSpecFile",
				fmt.Sprintf("spec is valid %s syntax but contains no recognized fields", p.name), ErrInvalidSpec)
		}
		return spec, nil
	}

	parseErrs = append(parseErrs, ErrInvalidSpec)
	return nil, E("store.parseSpecFile", "parsing spec as JSON or YAML", errors.Join(parseErrs...))
}

func isSpecPopulated(spec *SpecFile) bool {
	return spec.Image != nil ||
		len(spec.Env) > 0 ||
		spec.CPUs != nil ||
		spec.Memory != nil ||
		spec.PidsLimit != nil ||
		spec.AllowNetwork != nil
}

type Sandbox struct {
	ID         int       `json:"id" yaml:"id"`
	PlatformID string    `json:"platform_id" yaml:"platform_id"`
	State      TaskState `json:"state" yaml:"state"`
	Spec       SpecFile  `json:"spec" yaml:"spec"`
	CreatedAt  time.Time `json:"created_at" yaml:"created_at"`
	UpdatedAt  time.Time `json:"updated_at" yaml:"updated_at"`
}

type IdempotencyKey struct {
	ID        int       `json:"id" yaml:"id"`
	SandboxID string    `json:"sandbox_id" yaml:"sandbox_id"`
	Key       string    `json:"key" yaml:"key"`
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`
	UpdatedAt time.Time `json:"updated_at" yaml:"updated_at"`
}
