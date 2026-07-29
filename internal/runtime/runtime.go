package runtime

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"time"
)

type Runtime interface {
	Create(ctx context.Context, spec Spec) (Info, error)
	Inspect(ctx context.Context, platformID string) (Info, error)
	List(ctx context.Context) ([]Info, error)
	// Destroy of an unknown id return nil; cleanup needs to be retry safe
	Destroy(ctx context.Context, platformID string) error
	Exec(ctx context.Context, platformID string, cmd []string, opts ExecOpts) (ExecResult, error)
	WriteFile(ctx context.Context, platformID, path string, content []byte, mode fs.FileMode) error
	// ReadFile(ctx context.Context, platformID, path string) ([]byte, error)
	// ListDir(ctx context.Context, platformID, path string) ([]FileInfo, error)
	// RemovePath(ctx context.Context, platformID, path string) error
}

type Spec struct {
	Image        string
	Env          map[string]string
	CPULimit     float64
	MemoryLimit  int64
	PidsLimit    int64
	AllowNetwork bool
}

func NewSpec(img string, env map[string]string, cpuLimit float64, memoryLimit int64, pidsLimit int64, allowNetwork bool) Spec {
	return Spec{
		Env:          env,
		Image:        img,
		CPULimit:     cpuLimit,
		MemoryLimit:  memoryLimit,
		PidsLimit:    pidsLimit,
		AllowNetwork: allowNetwork,
	}
}

// The daemon rejects limits below these floors, so validating them here turns a
// round trip and a backend-shaped error into a local ErrInvalidSpec. The memory
// floor is Docker's own minimum for a cgroup memory limit; the CPU floor is the
// smallest NanoCPUs the daemon accepts.
const (
	MinMemoryLimit = 6 * 1024 * 1024 // 6MiB, in bytes
	MinCPULimit    = 0.01            // cores
)

// Validate rejects a Spec whose limits would not be enforced. Zero is not
// treated as "unset" for any of the three: Docker reads a zero memory, CPU, or
// pids limit as unlimited, so a field the caller forgot and a field the caller
// meant to leave open are indistinguishable at the backend. Requiring a positive
// value makes forgetting one a construction error rather than a sandbox that
// silently runs without a cgroup ceiling.
//
// It is a method on Spec rather than a Docker helper because the limits are
// quickspin's contract, not Docker's; every backend translates the same three
// numbers onto whatever its kernel interface is.
func (s Spec) Validate() error {
	const op = "runtime.Spec.Validate"

	switch {
	case s.Image == "":
		return E(op, "image is required", ErrInvalidSpec)
	case s.CPULimit < MinCPULimit:
		return E(op, fmt.Sprintf("cpu limit %g cores is below the minimum %g", s.CPULimit, MinCPULimit), ErrInvalidSpec)
	case s.MemoryLimit < MinMemoryLimit:
		return E(op, fmt.Sprintf("memory limit %d bytes is below the minimum %d", s.MemoryLimit, MinMemoryLimit), ErrInvalidSpec)
	case s.PidsLimit < 1:
		return E(op, fmt.Sprintf("pids limit %d must be positive", s.PidsLimit), ErrInvalidSpec)
	}
	return nil
}

type State string

const (
	StateRunning State = "running"
	StateStopped State = "stopped"
)

type Info struct {
	ID        string    `json:"id" yaml:"id"`
	State     State     `json:"state" yaml:"state"`
	CreatedAt time.Time `json:"created_at" yaml:"created_at"`
}

// NewInfo takes createdAt rather than reading the clock: the backend knows when
// the sandbox was actually created, and a constructor that stamps time.Now()
// reports the moment of observation instead — making every Info in a listing
// look microseconds apart and any sort by age meaningless.
func NewInfo(id string, state State, createdAt time.Time) Info {
	return Info{
		ID:        id,
		State:     state,
		CreatedAt: createdAt,
	}
}

type ExecOpts struct {
	Env     map[string]string
	WorkDir string
	Timeout time.Duration
}

// DefaultExecTimeout is what a zero or negative ExecOpts.Timeout means. It is
// the contract's fallback, not the CLI's, so a caller that never touches cobra
// gets the same bound.
const DefaultExecTimeout = 30 * time.Second

// MaxStreamBytes caps each captured stream. Plan 03 commits to buffered output
// with a cap rather than streaming: a command that writes without bound would
// otherwise be a way for a sandbox to exhaust the host's memory through the very
// call meant to contain it. Streaming is deferred to the guest-API plan so it is
// built once, in the right place.
const MaxStreamBytes = 1 << 20 // 1 MiB per stream

type ExecResult struct {
	ExitCode int
	Stdout   []byte
	Stderr   []byte

	// Truncation is reported per stream rather than as one flag: a caller
	// parsing stdout needs to know its own stream was cut, and a noisy stderr
	// hitting the cap says nothing about stdout's completeness. Silently
	// returning a short buffer is the failure these exist to prevent — a
	// truncated JSON document is indistinguishable from a malformed one.
	StdoutTruncated bool
	StderrTruncated bool
}

var ErrExecTimeout = errors.New("exec deadline exceeded")

// ErrExecNotKilled is joined onto the timeout rather than replacing it, so
// errors.Is still finds ErrExecTimeout.
var ErrExecNotKilled = errors.New("exec could not be stopped and may still be running")
