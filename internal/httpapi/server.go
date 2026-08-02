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
	"github.com/nickstrad/quickspin/internal/api"
	"github.com/nickstrad/quickspin/internal/control"
	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/nickstrad/quickspin/internal/sandbox"
	"github.com/nickstrad/quickspin/internal/store"
)

const (
	sandboxIDParam = "sandboxID"

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
	control *control.Control
}

// The store and runtime are kept alongside control because the read and
// filesystem routes call them directly; only the multi-step operations go
// through control.
func NewAPI(host string, port int, logger *slog.Logger, store store.Store, runtime runtime.Runtime) API {
	return API{
		Address: host,
		Port:    port,
		logger:  logger.With("subcomponent", "api"),
		store:   store,
		runtime: runtime,
		control: control.New(logger, store, runtime),
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

	if writeErr := api.WriteError(w, status, code, message); writeErr != nil {
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
// from the status by api.WriteError, so api.CodeForStatus stays the only status→code
// table.
func classify(err error) (status int, message string) {
	switch {
	case errors.Is(err, errInternal), errors.Is(err, control.ErrInternal):
		return http.StatusInternalServerError, "the request could not be completed"
	case errors.Is(err, runtime.ErrImageMissing):
		return http.StatusUnprocessableEntity, "the requested image does not exist"
	case errors.Is(err, sandbox.ErrInvalidSpec), errors.Is(err, runtime.ErrInvalidSpec):
		return http.StatusUnprocessableEntity, "sandbox spec is invalid"
	case errors.Is(err, sandbox.ErrInvalidStateTransition),
		errors.Is(err, sandbox.ErrInvalidState),
		errors.Is(err, sandbox.ErrSandboxNotRunning):
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
		a.fail(w, r, http.StatusBadRequest, api.CodeInvalidRequest,
			"request body is not a valid "+what,
			E(op, "decoding request body", err))
		return v, false
	}
	return v, true
}

func decodeOptionalBody[T any](a *API, w http.ResponseWriter, r *http.Request, op, what string) (T, bool) {
	if r.Body == http.NoBody {
		var zero T
		return zero, true
	}
	return decodeBody[T](a, w, r, op, what)
}

// queryPath reads the path query parameter the file and directory GET routes
// carry instead of a body, which proxies may drop on a GET.
func (a *API) queryPath(w http.ResponseWriter, r *http.Request, op string) (string, bool) {
	path := r.URL.Query().Get("path")
	if path == "" {
		a.fail(w, r, http.StatusBadRequest, api.CodeInvalidRequest,
			"the path query parameter is required",
			E(op, "missing path", ErrInvalidRequest))
		return "", false
	}
	return path, true
}

func (a *API) CreateSandbox(w http.ResponseWriter, r *http.Request) {
	const op = "httpapi.API.CreateSandbox"
	ctx := r.Context()

	idempotencyKey := r.Header.Get(api.IdempotencyKeyHeader)
	if idempotencyKey == "" {
		a.fail(w, r, http.StatusBadRequest, api.CodeInvalidRequest,
			"Idempotency-Key header is required",
			E(op, "missing idempotency key", ErrInvalidRequest))
		return
	}

	req, ok := decodeBody[api.CreateSandboxRequest](a, w, r, op, "sandbox create request")
	if !ok {
		return
	}

	sbx, err := a.control.CreateSandbox(ctx, idempotencyKey, req.Spec, req.TTL())
	if err != nil {
		a.failWith(w, r, Wrap(op, "creating the sandbox", err))
		return
	}

	// 202, not 201: the row exists but the sandbox does not yet run, and the
	// reconciler is what closes that gap.
	a.respond(w, r, http.StatusAccepted, api.NewSandboxResponse(sbx))
}

func (a *API) respond(w http.ResponseWriter, r *http.Request, status int, v any) {
	if err := api.WriteJSON(w, status, v); err != nil {
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

	resp := make([]api.SandboxResponse, len(sbxs))
	for i, sbx := range sbxs {
		resp[i] = api.NewSandboxResponse(sbx)
	}
	a.respond(w, r, http.StatusOK, resp)
}

func (a *API) GetSandboxEvents(w http.ResponseWriter, r *http.Request) {
	const op = "httpapi.API.GetSandboxEvents"
	ctx := r.Context()
	sandboxID := chi.URLParam(r, sandboxIDParam)

	// Event lookup deliberately returns an empty slice for an unknown id, so
	// existence must be established separately to preserve resource semantics.
	if _, err := a.store.GetSandbox(ctx, sandboxID); err != nil {
		a.failWith(w, r, Wrap(op, "loading the sandbox", err))
		return
	}

	events, err := a.store.GetSandboxEvents(ctx, sandboxID)
	if err != nil {
		a.failInternal(w, r, op, "listing sandbox events", err)
		return
	}

	resp := make([]api.SandboxEventResponse, len(events))
	for i, event := range events {
		resp[i] = api.NewSandboxEventResponse(event)
	}
	a.respond(w, r, http.StatusOK, resp)
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

	a.respond(w, r, http.StatusOK, api.NewInfoResponse(infoObjs))
}

func (a *API) DestroySandbox(w http.ResponseWriter, r *http.Request) {
	const op = "httpapi.API.DestroySandbox"
	ctx := r.Context()
	sandboxID := chi.URLParam(r, sandboxIDParam)

	if err := a.control.DestroySandbox(ctx, sandboxID); err != nil {
		a.failWith(w, r, Wrap(op, "destroying the sandbox", err))
		return
	}

	// 202, not 204: the row says stopping but the container may still be up, and
	// the reconciler is what closes that gap.
	a.respond(w, r, http.StatusAccepted, nil)
}

func (a *API) KeepaliveSandbox(w http.ResponseWriter, r *http.Request) {
	const op = "httpapi.API.KeepaliveSandbox"
	sandboxID := chi.URLParam(r, sandboxIDParam)

	req, ok := decodeOptionalBody[api.KeepaliveSandboxRequest](a, w, r, op, "sandbox keepalive request")
	if !ok {
		return
	}

	sbx, err := a.control.KeepaliveSandbox(r.Context(), sandboxID, req.TTL())
	if err != nil {
		a.failWith(w, r, Wrap(op, "renewing the sandbox lease", err))
		return
	}

	a.respond(w, r, http.StatusOK, api.NewSandboxResponse(sbx))
}

func (a *API) ExecInSandbox(w http.ResponseWriter, r *http.Request) {
	const op = "httpapi.API.ExecInSandbox"
	ctx := r.Context()
	sandboxID := chi.URLParam(r, sandboxIDParam)

	req, ok := decodeBody[api.ExecRequest](a, w, r, op, "exec request")
	if !ok {
		return
	}

	if len(req.Command) == 0 {
		a.fail(w, r, http.StatusBadRequest, api.CodeInvalidRequest,
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

	a.respond(w, r, http.StatusOK, api.NewExecResponse(execResult))
}

func (a *API) WriteInSandbox(w http.ResponseWriter, r *http.Request) {
	const op = "httpapi.API.WriteInSandbox"
	ctx := r.Context()
	sandboxID := chi.URLParam(r, sandboxIDParam)

	req, ok := decodeBody[api.WriteInSandboxRequest](a, w, r, op, "write in sandbox request")
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
	if err := a.control.RequireRunning(r.Context(), sandboxID); err != nil {
		a.failWith(w, r, Wrap(op, "checking the sandbox is running", err))
		return false
	}
	return true
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

	resp := make([]api.FileInfoResponse, len(fileInfoObjs))
	for i, info := range fileInfoObjs {
		resp[i] = api.NewFileInfoResponse(info)
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
			r.Get("/events", a.GetSandboxEvents)
			r.Delete("/", a.DestroySandbox)
			r.Post("/keepalive", a.KeepaliveSandbox)
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
