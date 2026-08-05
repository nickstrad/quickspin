// Package api is the wire protocol of the control plane: the request and
// response DTOs, the error envelope, and the converters between them and the
// domain types. It is owned by neither the server nor the client — httpapi
// encodes these types and client decodes them, so the client no longer has a
// compile-time dependency on the server.
package api

import (
	"io/fs"
	"time"

	"github.com/nickstrad/quickspin/internal/events"
	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/nickstrad/quickspin/internal/sandbox"
)

// Currently experimental but industry is using it:
// https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Idempotency-Key
const IdempotencyKeyHeader = "Idempotency-Key"

// CreateSandboxRequest carries the lifetime alongside the spec. The lifetime is
// a TTL rather than an absolute instant because the server's clock is the one
// that decides when the sandbox dies; zero means the server default. Whole
// seconds for the same reason ExecOptions uses them.
type CreateSandboxRequest struct {
	Spec       sandbox.SpecFile `json:"spec"`
	TTLSeconds int64            `json:"ttl_seconds,omitempty"`
}

func NewCreateSandboxRequest(spec sandbox.SpecFile, ttl time.Duration) CreateSandboxRequest {
	return CreateSandboxRequest{Spec: spec, TTLSeconds: ceilSeconds(ttl)}
}

func (r CreateSandboxRequest) TTL() time.Duration {
	return time.Duration(r.TTLSeconds) * time.Second
}

// KeepaliveSandboxRequest carries the renewed lifetime measured from the
// server's receipt of the request. Zero takes the default; the control layer
// rejects negative values and clamps values above the platform cap.
type KeepaliveSandboxRequest struct {
	TTLSeconds int64 `json:"ttl_seconds,omitempty"`
}

func NewKeepaliveSandboxRequest(ttl time.Duration) KeepaliveSandboxRequest {
	seconds := ceilSeconds(ttl)
	if ttl < 0 {
		// Preserve invalid negative durations across the wire so the server can
		// reject them instead of silently treating them as an omitted value. A
		// sub-second negative still needs a nonzero representation.
		seconds = min(int64(ttl/time.Second), -1)
	}
	return KeepaliveSandboxRequest{TTLSeconds: seconds}
}

func (r KeepaliveSandboxRequest) TTL() time.Duration {
	return time.Duration(r.TTLSeconds) * time.Second
}

// SandboxResponse is the wire form of sandbox.Sandbox. Spec stays sandbox.SpecFile
// unconverted: the spec document is already the deliberate user-facing format
// that CreateSandbox accepts. The store's integer row id never travels.
type SandboxResponse struct {
	SandboxID string           `json:"sandbox_id"`
	VersionID int              `json:"version_id"`
	State     string           `json:"state"`
	Spec      sandbox.SpecFile `json:"spec"`
	ExpiresAt time.Time        `json:"expires_at"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
}

func NewSandboxResponse(sbx *sandbox.Sandbox) SandboxResponse {
	return SandboxResponse{
		SandboxID: sbx.SandboxID,
		VersionID: sbx.VersionID,
		State:     string(sbx.State),
		Spec:      sbx.Spec,
		ExpiresAt: sbx.ExpiresAt,
		CreatedAt: sbx.CreatedAt,
		UpdatedAt: sbx.UpdatedAt,
	}
}

func (s SandboxResponse) Sandbox() *sandbox.Sandbox {
	return &sandbox.Sandbox{
		SandboxID: s.SandboxID,
		VersionID: s.VersionID,
		State:     sandbox.TaskState(s.State),
		Spec:      s.Spec,
		ExpiresAt: s.ExpiresAt,
		CreatedAt: s.CreatedAt,
		UpdatedAt: s.UpdatedAt,
	}
}

// SandboxEventResponse is the wire form of events.Event. The store's global id
// stays internal; version is the per-sandbox history order.
type SandboxEventResponse struct {
	SandboxID string    `json:"sandbox_id"`
	VersionID int       `json:"version_id"`
	FromState string    `json:"from_state"`
	ToState   string    `json:"to_state"`
	At        time.Time `json:"at"`
	Reason    string    `json:"reason"`
}

func NewSandboxEventResponse(event *events.Event) SandboxEventResponse {
	return SandboxEventResponse{
		SandboxID: event.SandboxID,
		VersionID: event.VersionID,
		FromState: string(event.FromState),
		ToState:   string(event.ToState),
		At:        event.At,
		Reason:    event.Reason,
	}
}

func (e SandboxEventResponse) Event() *events.Event {
	return &events.Event{
		SandboxID: e.SandboxID,
		VersionID: e.VersionID,
		FromState: sandbox.TaskState(e.FromState),
		ToState:   sandbox.TaskState(e.ToState),
		At:        e.At,
		Reason:    e.Reason,
	}
}

// InfoResponse is the wire form of runtime.Info.
type InfoResponse struct {
	ID        string    `json:"id"`
	State     string    `json:"state"`
	CreatedAt time.Time `json:"created_at"`
}

func NewInfoResponse(info runtime.Info) InfoResponse {
	return InfoResponse{
		ID:        info.ID,
		State:     string(info.State),
		CreatedAt: info.CreatedAt,
	}
}

func (i InfoResponse) Info() runtime.Info {
	return runtime.Info{
		ID:        i.ID,
		State:     runtime.State(i.State),
		CreatedAt: i.CreatedAt,
	}
}

type ExecRequest struct {
	Command []string    `json:"command"`
	Options ExecOptions `json:"options"`
}

// ExecOptions is the wire form of runtime.ExecOpts. Timeout travels as whole
// seconds because an untagged time.Duration would serialize as raw nanoseconds;
// zero means the server-side default.
type ExecOptions struct {
	Env            map[string]string `json:"env,omitempty"`
	WorkDir        string            `json:"work_dir,omitempty"`
	TimeoutSeconds int64             `json:"timeout_seconds,omitempty"`
}

func NewExecOptions(opts runtime.ExecOpts) ExecOptions {
	return ExecOptions{
		Env:            opts.Env,
		WorkDir:        opts.WorkDir,
		TimeoutSeconds: ceilSeconds(opts.Timeout),
	}
}

// Rounded up so a sub-second duration does not become zero, which would
// silently mean "use the default" on the server.
func ceilSeconds(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return int64((d + time.Second - 1) / time.Second)
}

func (o ExecOptions) ExecOpts() runtime.ExecOpts {
	return runtime.ExecOpts{
		Env:     o.Env,
		WorkDir: o.WorkDir,
		Timeout: time.Duration(o.TimeoutSeconds) * time.Second,
	}
}

// ExecResponse exists because runtime.ExecResult carries no JSON tags: returning
// it directly would publish Go field names as the wire contract by accident, and
// renaming a field in the runtime would then be a breaking API change.
type ExecResponse struct {
	ExitCode int `json:"exit_code"`
	// []byte marshals as base64, so binary output needs no per-byte JSON
	// escaping.
	Stdout          []byte `json:"stdout"`
	Stderr          []byte `json:"stderr"`
	StdoutTruncated bool   `json:"stdout_truncated"`
	StderrTruncated bool   `json:"stderr_truncated"`
}

func NewExecResponse(result runtime.ExecResult) ExecResponse {
	return ExecResponse{
		ExitCode:        result.ExitCode,
		Stdout:          result.Stdout,
		Stderr:          result.Stderr,
		StdoutTruncated: result.StdoutTruncated,
		StderrTruncated: result.StderrTruncated,
	}
}

func (r ExecResponse) ExecResult() runtime.ExecResult {
	return runtime.ExecResult{
		ExitCode:        r.ExitCode,
		Stdout:          r.Stdout,
		Stderr:          r.Stderr,
		StdoutTruncated: r.StdoutTruncated,
		StderrTruncated: r.StderrTruncated,
	}
}

type WriteInSandboxRequest struct {
	Path string `json:"path"`
	// []byte so the decoder reads base64 straight into bytes with no
	// intermediate string copy of the whole file.
	Content  []byte `json:"content"`
	FileMode int64  `json:"fileMode"`
}

// FileInfoResponse is the wire form of runtime.FileInfo. Mode is the numeric
// fs.FileMode value, matching how WriteInSandboxRequest carries it.
type FileInfoResponse struct {
	Path  string `json:"path"`
	Size  int64  `json:"size"`
	Mode  int64  `json:"mode"`
	IsDir bool   `json:"is_dir"`
}

func NewFileInfoResponse(info runtime.FileInfo) FileInfoResponse {
	return FileInfoResponse{
		Path:  info.Path,
		Size:  info.Size,
		Mode:  int64(info.Mode),
		IsDir: info.IsDir,
	}
}

func (f FileInfoResponse) FileInfo() runtime.FileInfo {
	return runtime.FileInfo{
		Path:  f.Path,
		Size:  f.Size,
		Mode:  fs.FileMode(f.Mode),
		IsDir: f.IsDir,
	}
}
