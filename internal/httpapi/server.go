package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/nickstrad/quickspin/internal/store"
)

const (
	// Currently experimental but industry is using it:
	// https://developer.mozilla.org/en-US/docs/Web/HTTP/Reference/Headers/Idempotency-Key
	IdempotencyKeyHeader = "Idempotency-Key"
	sandboxIDParam       = "sandboxID"

	// Sized so a runtime.MaxFileSize file survives base64's 4/3 inflation plus
	// the rest of the write envelope.
	maxRequestBytes = runtime.MaxFileSize/3*4 + 4096

	readHeaderTimeout = 5 * time.Second
	idleTimeout       = 2 * time.Minute
	shutdownTimeout   = 10 * time.Second
)

type API struct {
	Address string
	Port    int
	Router  *chi.Mux
	Server  *http.Server
	logger  *slog.Logger
	store   store.Store
	runtime runtime.Runtime
}

func NewAPI(host string, port int, logger *slog.Logger, store store.Store, runtime runtime.Runtime) API {
	return API{
		Address: host,
		Port:    port,
		logger:  logger.With("subcomponent", "api"),
		store:   store,
		runtime: runtime,
	}
}

// fail is where an error stops being a Go value. It logs the whole chain once —
// the layers below report their own successful state changes but never their
// failures, so a handler is the only place a failure is recorded — then writes
// the public envelope. message is what the client reads; err is what the
// operator reads, and the two are deliberately not the same string.
func (a *API) fail(w http.ResponseWriter, r *http.Request, status int, code, message string, err error) {
	logger := a.logger.With(
		"method", r.Method,
		"url", r.URL.Path,
		"code", status,
		"requestID", middleware.GetReqID(r.Context()),
		"err", err,
	)
	// Recoverable from url, but only a structured field is greppable across the
	// several routes a single sandbox appears on.
	if sandboxID := chi.URLParam(r, sandboxIDParam); sandboxID != "" {
		logger = logger.With("sandboxID", sandboxID)
	}
	if op := OpOf(err); op != "" {
		logger = logger.With("op", op)
	}

	// A 4xx is the client's mistake and ordinary traffic; a 5xx is ours.
	if status >= http.StatusInternalServerError {
		logger.ErrorContext(r.Context(), "request failed")
	} else {
		logger.WarnContext(r.Context(), "request rejected")
	}

	if writeErr := WriteError(w, status, code, message); writeErr != nil {
		// The status line is already flushed, so the client gets a truncated
		// body regardless; all that is left is to record it.
		a.logger.ErrorContext(r.Context(), "writing error response failed", "url", r.URL.Path, "err", writeErr)
	}
}

// errInternal marks a failure whose sentinel would otherwise classify as a 4xx
// but which is ours, not the client's — a dangling idempotency key, a
// transition that should have been legal. Joined onto the chain so classify
// sees it before the misleading sentinel.
var errInternal = errors.New("internal failure")

// failWith is the propagation stop: it picks the status from the sentinels in
// the chain and writes the envelope. Handlers pass the wrapped error and say
// nothing about status codes, so one table decides what each sentinel means.
func (a *API) failWith(w http.ResponseWriter, r *http.Request, err error) {
	status, message := classify(err)
	a.fail(w, r, status, "", message, err)
}

// failInternal wraps and reports an error that must read as ours regardless of
// the sentinels it carries.
func (a *API) failInternal(w http.ResponseWriter, r *http.Request, op, message string, err error) {
	a.failWith(w, r, Wrap(op, message, errors.Join(errInternal, err)))
}

// classify lives here rather than in errors.go because errors.go is shared with
// the SDK clients; importing store and runtime there would drag the server's
// dependencies into every client. message is prose for the client — the
// operator reads the chain in the log instead. The envelope code is derived
// from the status by WriteError, so CodeForStatus stays the only status→code
// table.
func classify(err error) (status int, message string) {
	switch {
	case errors.Is(err, errInternal):
		return http.StatusInternalServerError, "the request could not be completed"
	case errors.Is(err, runtime.ErrImageMissing):
		return http.StatusUnprocessableEntity, "the requested image does not exist"
	case errors.Is(err, store.ErrInvalidSpec), errors.Is(err, runtime.ErrInvalidSpec):
		return http.StatusUnprocessableEntity, "sandbox spec is invalid"
	case errors.Is(err, store.ErrInvalidStateTransition),
		errors.Is(err, store.ErrInvalidState),
		errors.Is(err, store.ErrSandboxNotRunning):
		return http.StatusConflict, "the sandbox is not in a state that allows this"
	case errors.Is(err, runtime.ErrInvalidPath):
		return http.StatusUnprocessableEntity, "the path is not valid"
	case errors.Is(err, runtime.ErrPathNotFound):
		return http.StatusNotFound, "path not found in the sandbox"
	case errors.Is(err, runtime.ErrFileTooLarge), errors.Is(err, runtime.ErrTotalFilesTooLarge):
		return http.StatusRequestEntityTooLarge, "the file is larger than the server allows"
	case errors.Is(err, runtime.ErrExecTimeout):
		return http.StatusGatewayTimeout, "the command did not finish in time"
	// A malformed id is a 404 rather than a 400: whether an id is well-formed is
	// this server's business, and either way no such sandbox exists.
	case errors.Is(err, store.ErrNotFound),
		errors.Is(err, runtime.ErrNotFound),
		errors.Is(err, runtime.ErrInvalidSandboxID),
		errors.Is(err, ErrNotFound):
		return http.StatusNotFound, "sandbox not found"
	case errors.Is(err, ErrInvalidRequest):
		return http.StatusBadRequest, "the request is not valid"
	default:
		return http.StatusInternalServerError, "the request could not be completed"
	}
}

// decodeBody is a free function rather than a method because Go methods cannot
// take type parameters.
func decodeBody[T any](a *API, w http.ResponseWriter, r *http.Request, op, what string) (T, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()

	var v T
	if err := d.Decode(&v); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			a.fail(w, r, http.StatusRequestEntityTooLarge, "",
				"request body is larger than the server allows",
				E(op, "request body too large", err))
			return v, false
		}
		a.fail(w, r, http.StatusBadRequest, CodeInvalidRequest,
			"request body is not a valid "+what,
			E(op, "decoding request body", err))
		return v, false
	}
	return v, true
}

// queryPath reads the path query parameter the file and directory GET routes
// carry instead of a body, which proxies may drop on a GET.
func (a *API) queryPath(w http.ResponseWriter, r *http.Request, op string) (string, bool) {
	path := r.URL.Query().Get("path")
	if path == "" {
		a.fail(w, r, http.StatusBadRequest, CodeInvalidRequest,
			"the path query parameter is required",
			E(op, "missing path", ErrInvalidRequest))
		return "", false
	}
	return path, true
}

// SandboxResponse is the wire form of store.Sandbox. Spec stays store.SpecFile
// unconverted: the spec document is already the deliberate user-facing format
// that CreateSandbox accepts. The store's integer row id never travels.
type SandboxResponse struct {
	SandboxID string         `json:"sandbox_id"`
	State     string         `json:"state"`
	Spec      store.SpecFile `json:"spec"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

func NewSandboxResponse(sbx *store.Sandbox) SandboxResponse {
	return SandboxResponse{
		SandboxID: sbx.SandboxID,
		State:     string(sbx.State),
		Spec:      sbx.Spec,
		CreatedAt: sbx.CreatedAt,
		UpdatedAt: sbx.UpdatedAt,
	}
}

func (s SandboxResponse) Sandbox() *store.Sandbox {
	return &store.Sandbox{
		SandboxID: s.SandboxID,
		State:     store.TaskState(s.State),
		Spec:      s.Spec,
		CreatedAt: s.CreatedAt,
		UpdatedAt: s.UpdatedAt,
	}
}

func (a *API) CreateSandbox(w http.ResponseWriter, r *http.Request) {
	const op = "httpapi.API.CreateSandbox"
	ctx := r.Context()

	idempotencyKey := r.Header.Get(IdempotencyKeyHeader)
	if idempotencyKey == "" {
		a.fail(w, r, http.StatusBadRequest, CodeInvalidRequest,
			"Idempotency-Key header is required",
			E(op, "missing idempotency key", ErrInvalidRequest))
		return
	}

	spec, ok := decodeBody[store.SpecFile](a, w, r, op, "sandbox spec")
	if !ok {
		return
	}

	// Resolved before the store write so an unenforceable limit is a 422 with no
	// row behind it, rather than a pending sandbox that can never start.
	resolved, err := spec.Resolve()
	if err == nil {
		err = resolved.Validate()
	}
	if err != nil {
		a.failWith(w, r, Wrap(op, "resolving the submitted spec", err))
		return
	}

	sbx, err := a.store.CreateSandbox(ctx, idempotencyKey, spec)
	if err != nil {
		// A store.ErrNotFound here is not the client's problem: it means the
		// idempotency key points at a sandbox that no longer exists, which is
		// our inconsistency, so only an invalid spec is allowed to be a 4xx.
		if !errors.Is(err, store.ErrInvalidSpec) {
			err = errors.Join(errInternal, err)
		}
		a.failWith(w, r, Wrap(op, "recording the sandbox", err))
		return
	}

	// A repeated key returns the original sandbox, which already has a runtime;
	// starting a second one is exactly the duplicate the key exists to prevent.
	if sbx.State != store.Pending {
		a.logger.InfoContext(ctx, "returning the existing sandbox for a repeated idempotency key",
			"idempotencyKey", idempotencyKey, "sandboxID", sbx.SandboxID, "state", sbx.State)
		a.respond(w, r, http.StatusCreated, NewSandboxResponse(sbx))
		return
	}

	// The store minted the id and it is already durable, so the runtime is told
	// which sandbox it is building rather than inventing a second identity.
	if _, err := a.runtime.Create(ctx, sbx.SandboxID, resolved); err != nil {
		a.markFailed(ctx, sbx.SandboxID)
		a.failWith(w, r, Wrap(op, "starting the sandbox", err))
		return
	}

	// Create starts the container, so success means running rather than
	// created-but-idle.
	running, err := a.store.UpdateSandboxState(ctx, sbx.SandboxID, store.Pending, store.Running)
	if err != nil {
		a.failInternal(w, r, op, "recording the sandbox as running", err)
		return
	}

	a.respond(w, r, http.StatusCreated, NewSandboxResponse(running))
}

// markFailed records a create that the runtime refused. The error is logged
// rather than returned: the caller is already on its way to a 5xx, and a row
// left in pending is a reconciler's problem, not this request's.
func (a *API) markFailed(ctx context.Context, sandboxID string) {
	if _, err := a.store.UpdateSandboxState(ctx, sandboxID, store.Pending, store.Failed); err != nil {
		a.logger.ErrorContext(ctx, "marking the sandbox failed after the runtime refused it",
			"sandboxID", sandboxID, "err", err)
		return
	}
	a.logger.WarnContext(ctx, "sandbox marked failed after the runtime refused it", "sandboxID", sandboxID)
}

func (a *API) respond(w http.ResponseWriter, r *http.Request, status int, v any) {
	if err := WriteJSON(w, status, v); err != nil {
		a.logger.ErrorContext(r.Context(), "writing response failed", "url", r.URL.Path, "err", err)
	}
}

// The store's records, not the runtime's: the row is the authoritative
// statement of what should exist, and it can describe a pending or failed
// sandbox that has no container for runtime.List to report.
func (a *API) ListSandboxes(w http.ResponseWriter, r *http.Request) {
	const op = "httpapi.API.ListSandboxes"
	ctx := r.Context()

	sbxs, err := a.store.GetSandboxes(ctx)
	if err != nil {
		a.failInternal(w, r, op, "listing sandboxes", err)
		return
	}

	resp := make([]SandboxResponse, len(sbxs))
	for i, sbx := range sbxs {
		resp[i] = NewSandboxResponse(sbx)
	}
	a.respond(w, r, http.StatusOK, resp)
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

func (a *API) InspectSandbox(w http.ResponseWriter, r *http.Request) {
	const op = "httpapi.API.InspectSandbox"
	ctx := r.Context()
	sandboxID := chi.URLParam(r, sandboxIDParam)

	if !a.ensureRunning(w, r, op, sandboxID) {
		return
	}

	// failWith, not failInternal: a container the runtime cannot find is a 404
	// like it is everywhere else. Until the reconciler exists, a row that still
	// says running with no container behind it is an ordinary state, not a
	// server fault.
	infoObjs, err := a.runtime.Inspect(ctx, sandboxID)
	if err != nil {
		a.failWith(w, r, Wrap(op, fmt.Sprintf("inspecting sandbox %s", sandboxID), err))
		return
	}

	a.respond(w, r, http.StatusOK, NewInfoResponse(infoObjs))
}

func (a *API) DestroySandbox(w http.ResponseWriter, r *http.Request) {
	const op = "httpapi.API.DestroySandbox"
	ctx := r.Context()
	sandboxID := chi.URLParam(r, sandboxIDParam)

	// Running is the only state Stopping is reachable from, so naming it beats
	// reading the row to feed its own state back in: the store's WHERE gate
	// rejects every other case and says whether the id was absent or the state
	// was wrong.
	if _, err := a.store.UpdateSandboxState(ctx, sandboxID, store.Running, store.Stopping); err != nil {
		// An absent row or one already past Running means the sandbox is not
		// running, which is the outcome the caller asked for — DELETE is
		// idempotent, so that is a success, not an error.
		if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrInvalidStateTransition) {
			a.logger.InfoContext(ctx, "destroy found nothing to stop",
				"sandboxID", sandboxID, "err", err)
			a.respond(w, r, http.StatusNoContent, nil)
			return
		}
		a.failWith(w, r, Wrap(op, "marking the sandbox stopping", err))
		return
	}

	if err := a.runtime.Destroy(ctx, sandboxID); err != nil {
		a.failWith(w, r, Wrap(op, "destroying the sandbox", err))
		return
	}

	// The container is gone by now, so a row still in stopping is our
	// inconsistency rather than anything the client did wrong.
	if _, err := a.store.UpdateSandboxState(ctx, sandboxID, store.Stopping, store.Stopped); err != nil {
		a.failInternal(w, r, op, "recording the sandbox as stopped", err)
		return
	}

	a.respond(w, r, http.StatusNoContent, nil)
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
	var secs int64
	if opts.Timeout > 0 {
		// Rounded up so a sub-second timeout does not become zero, which would
		// silently mean "use the default" on the server.
		secs = int64((opts.Timeout + time.Second - 1) / time.Second)
	}
	return ExecOptions{
		Env:            opts.Env,
		WorkDir:        opts.WorkDir,
		TimeoutSeconds: secs,
	}
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

func (a *API) ExecInSandbox(w http.ResponseWriter, r *http.Request) {
	const op = "httpapi.API.ExecInSandbox"
	ctx := r.Context()
	sandboxID := chi.URLParam(r, sandboxIDParam)

	req, ok := decodeBody[ExecRequest](a, w, r, op, "exec request")
	if !ok {
		return
	}

	if len(req.Command) == 0 {
		a.fail(w, r, http.StatusBadRequest, CodeInvalidRequest,
			"command is required",
			E(op, "missing command string", ErrInvalidRequest))
		return
	}

	if !a.ensureRunning(w, r, op, sandboxID) {
		return
	}

	execResult, err := a.runtime.Exec(ctx, sandboxID, req.Command, req.Options.ExecOpts())
	if err != nil {
		a.failWith(w, r, Wrap(op, fmt.Sprintf("executing cmd %s in sandbox %s", strings.Join(req.Command, " "), sandboxID), err))
		return
	}

	a.respond(w, r, http.StatusOK, NewExecResponse(execResult))
}

type WriteInSandboxRequest struct {
	Path string `json:"path"`
	// []byte so the decoder reads base64 straight into bytes with no
	// intermediate string copy of the whole file.
	Content  []byte `json:"content"`
	FileMode int64  `json:"fileMode"`
}

func (a *API) WriteInSandbox(w http.ResponseWriter, r *http.Request) {
	const op = "httpapi.API.WriteInSandbox"
	ctx := r.Context()
	sandboxID := chi.URLParam(r, sandboxIDParam)

	req, ok := decodeBody[WriteInSandboxRequest](a, w, r, op, "write in sandbox request")
	if !ok {
		return
	}

	if !a.ensureRunning(w, r, op, sandboxID) {
		return
	}

	if err := a.runtime.WriteFile(ctx, sandboxID, req.Path, req.Content, fs.FileMode(req.FileMode)); err != nil {
		a.failWith(w, r, Wrap(op, fmt.Sprintf("writing file %s in sandbox %s", req.Path, sandboxID), err))
		return
	}

	a.respond(w, r, http.StatusNoContent, nil)
}

// ensureRunning gates a handler on the sandbox being in the running state and
// writes the failure itself, so callers only decide whether to continue. The
// status comes from classify: not running is a 409, an unknown id a 404, a
// store failure a 500.
func (a *API) ensureRunning(w http.ResponseWriter, r *http.Request, op, sandboxID string) bool {
	if err := a.requireRunning(r.Context(), sandboxID); err != nil {
		a.failWith(w, r, Wrap(op, "checking the sandbox is running", err))
		return false
	}
	return true
}

func (a *API) requireRunning(ctx context.Context, sandboxID string) error {
	const op = "httpapi.API.requireRunning"
	sbx, err := a.store.GetSandbox(ctx, sandboxID)
	if err != nil {
		return Wrap(op, "loading the sandbox", err)
	}

	if sbx.State != store.Running {
		return Wrap(op, "", store.ErrSandboxNotRunning)
	}
	return nil
}

func (a *API) ReadFromSandbox(w http.ResponseWriter, r *http.Request) {
	const op = "httpapi.API.ReadFromSandbox"
	ctx := r.Context()
	sandboxID := chi.URLParam(r, sandboxIDParam)

	path, ok := a.queryPath(w, r, op)
	if !ok {
		return
	}

	if !a.ensureRunning(w, r, op, sandboxID) {
		return
	}

	valInBytes, err := a.runtime.ReadFile(ctx, sandboxID, path)
	if err != nil {
		a.failWith(w, r, Wrap(op, fmt.Sprintf("reading file %s in sandbox %s", path, sandboxID), err))
		return
	}

	// []byte marshals as base64, so binary content needs no per-byte JSON
	// escaping and no intermediate string copy.
	a.respond(w, r, http.StatusOK, valInBytes)
}

// RemoveFromSandbox deletes a path. It is not idempotent the way DELETE on a
// sandbox is: a missing path answers 404 rather than 204, because the runtime
// reports it and the caller asked about one specific file rather than about a
// resource this API owns the lifecycle of.
func (a *API) RemoveFromSandbox(w http.ResponseWriter, r *http.Request) {
	const op = "httpapi.API.RemoveFromSandbox"
	ctx := r.Context()
	sandboxID := chi.URLParam(r, sandboxIDParam)

	path, ok := a.queryPath(w, r, op)
	if !ok {
		return
	}

	if !a.ensureRunning(w, r, op, sandboxID) {
		return
	}

	if err := a.runtime.RemovePath(ctx, sandboxID, path); err != nil {
		a.failWith(w, r, Wrap(op, fmt.Sprintf("removing path %s in sandbox %s", path, sandboxID), err))
		return
	}

	a.respond(w, r, http.StatusNoContent, nil)
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

func (a *API) ListFromSandbox(w http.ResponseWriter, r *http.Request) {
	const op = "httpapi.API.ListFromSandbox"
	ctx := r.Context()
	sandboxID := chi.URLParam(r, sandboxIDParam)

	path, ok := a.queryPath(w, r, op)
	if !ok {
		return
	}

	if !a.ensureRunning(w, r, op, sandboxID) {
		return
	}

	fileInfoObjs, err := a.runtime.ListDir(ctx, sandboxID, path)
	if err != nil {
		a.failWith(w, r, Wrap(op, fmt.Sprintf("reading directory %s in sandbox %s", path, sandboxID), err))
		return
	}

	resp := make([]FileInfoResponse, len(fileInfoObjs))
	for i, info := range fileInfoObjs {
		resp[i] = NewFileInfoResponse(info)
	}
	a.respond(w, r, http.StatusOK, resp)
}

// Handler builds the routes on first use and returns them, so a test in another
// package can wrap the API in an httptest.Server without opening a socket of
// its own.
func (a *API) Handler() http.Handler {
	if a.Router == nil {
		a.initRouter()
	}
	return a.Router
}

func (a *API) initRouter() {
	a.Router = chi.NewRouter()
	// RequestID first so both the access log and any failure below carry the
	// same id; concurrent requests are otherwise impossible to separate.
	a.Router.Use(middleware.RequestID, a.logRequests)
	a.Router.Route("/v1/sandboxes", func(r chi.Router) {
		r.Post("/", a.CreateSandbox)
		r.Get("/", a.ListSandboxes)
		r.Route(fmt.Sprintf("/{%s}", sandboxIDParam), func(r chi.Router) {
			r.Get("/", a.InspectSandbox)
			r.Delete("/", a.DestroySandbox)
			r.Post("/exec", a.ExecInSandbox)
			r.Put("/files", a.WriteInSandbox)
			r.Get("/files", a.ReadFromSandbox)
			r.Delete("/files", a.RemoveFromSandbox)
			r.Get("/dir", a.ListFromSandbox)
		})
	})
}

// Start binds the socket before serving so a port already in use is returned to
// the caller rather than lost in a goroutine. It also means the listening log
// below is a fact: it is written after the bind, and it names the address the
// kernel actually assigned, which is the only way to learn the port when Port
// is 0.
func (a *API) Start(done <-chan struct{}) error {
	const op = "httpapi.API.Start"
	addr := fmt.Sprintf("%s:%d", a.Address, a.Port)

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return E(op, fmt.Sprintf("listening on %s", addr), err)
	}

	// No ReadTimeout or WriteTimeout: exec can legitimately run long, and the
	// header and idle timeouts already bound what an idle client can hold open.
	a.Server = &http.Server{
		Handler:           a.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		IdleTimeout:       idleTimeout,
	}

	a.logger.Info("control plane listening", "addr", listener.Addr().String())

	// Buffered so the goroutine can exit even when done fires first and nobody
	// is left reading.
	serveErr := make(chan error, 1)
	go func() { serveErr <- a.Server.Serve(listener) }()

	select {
	case err := <-serveErr:
		// Serve only returns ErrServerClosed once Stop has run, and Stop is not
		// what got us here, so every error on this path is a real one.
		return E(op, "serving requests", err)
	case <-done:
		return a.Stop()
	}
}

// Stop drains in-flight requests but gives up after shutdownTimeout: a
// long-running exec must not hold the process open forever on exit. A timeout
// is returned rather than swallowed — it means a request was still running when
// the process gave up on it.
func (a *API) Stop() error {
	if a.Server == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()

	if err := a.Server.Shutdown(ctx); err != nil {
		return E("httpapi.API.Stop", "shutting down the server", err)
	}
	return nil
}
