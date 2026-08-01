package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/nickstrad/quickspin/internal/runtime/runtimetest"
	"github.com/nickstrad/quickspin/internal/store"
)

func newTestAPI(t *testing.T) *API {
	t.Helper()
	return newTestAPIWithStore(t, newTestStore(t))
}

func newTestStore(t *testing.T) *store.SqlliteStore {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.NewSqlliteStore(context.Background(), ":memory:", "", logger)
	if err != nil {
		t.Fatalf("NewSqlliteStore(:memory:) error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := st.Cleanup(); err != nil {
			t.Errorf("Cleanup() error = %v, want nil", err)
		}
	})
	return st
}

func newTestAPIWithStore(t *testing.T, st store.Store) *API {
	t.Helper()

	// A create that succeeds, so a case not about the runtime still reaches the
	// pending-to-running transition. Inspect and Destroy are scripted too so
	// lifecycle tests can walk create → inspect → delete without a daemon.
	return newTestAPIWithRuntime(t, st, runtimetest.Fake{
		CreateFn: func(context.Context, string, runtime.Spec) (runtime.Info, error) {
			return runtime.Info{}, nil
		},
		InspectFn: func(_ context.Context, sandboxID string) (runtime.Info, error) {
			return runtime.Info{ID: sandboxID, State: runtime.StateRunning}, nil
		},
		DestroyFn: func(context.Context, string) error {
			return nil
		},
	})
}

func newTestAPIWithRuntime(t *testing.T, st store.Store, rt runtime.Runtime) *API {
	t.Helper()

	api := NewAPI("127.0.0.1", 0, slog.New(slog.NewTextHandler(io.Discard, nil)), st, rt)
	api.Handler()
	return &api
}

// fakeStore scripts one method at a time. Every unset method is nil, so a
// handler reaching for one panics the test rather than silently taking a path
// the case did not intend.
type fakeStore struct {
	store.Store
	createSandbox      func(ctx context.Context, key string, spec store.SpecFile) (*store.Sandbox, error)
	getSandbox         func(ctx context.Context, sandboxID string) (*store.Sandbox, error)
	getSandboxes       func(ctx context.Context) ([]*store.Sandbox, error)
	updateSandboxState func(ctx context.Context, sandboxID string, from, to store.TaskState) (*store.Sandbox, error)
}

func (f *fakeStore) CreateSandbox(ctx context.Context, key string, spec store.SpecFile) (*store.Sandbox, error) {
	return f.createSandbox(ctx, key, spec)
}

func (f *fakeStore) GetSandbox(ctx context.Context, sandboxID string) (*store.Sandbox, error) {
	return f.getSandbox(ctx, sandboxID)
}

func (f *fakeStore) GetSandboxes(ctx context.Context) ([]*store.Sandbox, error) {
	return f.getSandboxes(ctx)
}

func (f *fakeStore) UpdateSandboxState(ctx context.Context, sandboxID string, from, to store.TaskState) (*store.Sandbox, error) {
	return f.updateSandboxState(ctx, sandboxID, from, to)
}

func do(t *testing.T, api *API, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()

	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	rec := httptest.NewRecorder()
	api.Router.ServeHTTP(rec, req)
	return rec
}

func withKey(key string) map[string]string {
	return map[string]string{IdempotencyKeyHeader: key}
}

// mustCreate returns the decoded record so later requests can address the
// sandbox by the id the API itself handed out.
func mustCreate(t *testing.T, api *API, key, body string) map[string]any {
	t.Helper()

	rec := do(t, api, http.MethodPost, "/v1/sandboxes", body, withKey(key))
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST /v1/sandboxes = %d, want %d (body %s)", rec.Code, http.StatusCreated, rec.Body.String())
	}
	return decodeObject(t, rec)
}

func decodeObject(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response %q error = %v, want nil", rec.Body.String(), err)
	}
	return got
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) ErrorResponse {
	t.Helper()

	var got ErrorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding error envelope %q error = %v, want nil", rec.Body.String(), err)
	}
	if got.Error.Code == "" {
		t.Errorf("error envelope %q has an empty code", rec.Body.String())
	}
	if got.Error.Message == "" {
		t.Errorf("error envelope %q has an empty message", rec.Body.String())
	}
	return got
}

func sandboxID(t *testing.T, record map[string]any) string {
	t.Helper()

	id, ok := record["sandbox_id"].(string)
	if !ok || id == "" {
		t.Fatalf("record %#v has no sandbox_id", record)
	}
	return id
}

const alpineSpec = `{"image":"alpine:3.20"}`

func TestCreateSandboxReturnsCreatedRecord(t *testing.T) {
	api := newTestAPI(t)

	rec := do(t, api, http.MethodPost, "/v1/sandboxes", alpineSpec, withKey("k1"))

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusCreated, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	got := decodeObject(t, rec)
	if id := sandboxID(t, got); !strings.HasPrefix(id, "sbx_") {
		t.Errorf("sandbox_id = %q, want an sbx_-prefixed id", id)
	}
	// Pending is the row's initial state, not what the caller sees: the response
	// is written after the runtime create succeeds and the row transitions.
	if got["state"] != string(store.Running) {
		t.Errorf("state = %v, want %q", got["state"], store.Running)
	}
	spec, ok := got["spec"].(map[string]any)
	if !ok || spec["image"] != "alpine:3.20" {
		t.Errorf("spec = %#v, want the submitted image echoed back", got["spec"])
	}
}

func TestCreateSandboxWithNoFieldsUsesTheDefaultImage(t *testing.T) {
	st := newTestStore(t)

	var gotSpec runtime.Spec
	api := newTestAPIWithRuntime(t, st, runtimetest.Fake{
		CreateFn: func(_ context.Context, _ string, spec runtime.Spec) (runtime.Info, error) {
			gotSpec = spec
			return runtime.Info{}, nil
		},
	})

	record := mustCreate(t, api, "k1", `{}`)
	if gotSpec.Image != store.DefaultImage {
		t.Errorf("runtime image = %q, want %q", gotSpec.Image, store.DefaultImage)
	}

	// Stored specs remain unresolved so defaults can change independently.
	spec, ok := record["spec"].(map[string]any)
	if !ok || spec["image"] != nil {
		t.Errorf("echoed spec = %#v, want a null image", spec)
	}
}

// The autoincrement row id is an internal key; a client that learns it can
// enumerate every sandbox on the host.
func TestCreateSandboxDoesNotLeakRowID(t *testing.T) {
	api := newTestAPI(t)

	got := mustCreate(t, api, "k1", alpineSpec)

	if _, ok := got["id"]; ok {
		t.Errorf("response %#v contains the row id, want it omitted", got)
	}
}

func TestCreateSandboxRejectsBadRequests(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		body   string
		status int
		code   string
	}{
		{
			name:   "missing idempotency key",
			key:    "",
			body:   alpineSpec,
			status: http.StatusBadRequest,
			code:   CodeInvalidRequest,
		},
		{
			name:   "empty body",
			key:    "k1",
			body:   "",
			status: http.StatusBadRequest,
			code:   CodeInvalidRequest,
		},
		{
			name:   "malformed json",
			key:    "k1",
			body:   `{"image":`,
			status: http.StatusBadRequest,
			code:   CodeInvalidRequest,
		},
		{
			name:   "unknown field",
			key:    "k1",
			body:   `{"image":"alpine:3.20","gpus":4}`,
			status: http.StatusBadRequest,
			code:   CodeInvalidRequest,
		},
		{
			name:   "unrecognized keys only",
			key:    "k1",
			body:   `{"gpus":4}`,
			status: http.StatusBadRequest,
			code:   CodeInvalidRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newTestAPI(t)

			headers := map[string]string{}
			if tt.key != "" {
				headers[IdempotencyKeyHeader] = tt.key
			}
			rec := do(t, api, http.MethodPost, "/v1/sandboxes", tt.body, headers)

			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.status, rec.Body.String())
			}
			if got := decodeError(t, rec); got.Error.Code != tt.code {
				t.Errorf("error code = %q, want %q", got.Error.Code, tt.code)
			}
		})
	}
}

func TestCreateSandboxIsIdempotentAcrossRequests(t *testing.T) {
	api := newTestAPI(t)

	first := mustCreate(t, api, "same-operation", alpineSpec)
	second := mustCreate(t, api, "same-operation", `{"image":"debian:12"}`)

	if sandboxID(t, second) != sandboxID(t, first) {
		t.Errorf("retry sandbox_id = %q, want the original %q", sandboxID(t, second), sandboxID(t, first))
	}

	// The key deduplicates the side effect, not the request: the second body is
	// ignored and the caller sees the original sandbox as it is now.
	spec := second["spec"].(map[string]any)
	if spec["image"] != "alpine:3.20" {
		t.Errorf("retry spec.image = %v, want the original alpine:3.20", spec["image"])
	}

	list := do(t, api, http.MethodGet, "/v1/sandboxes", "", nil)
	var records []map[string]any
	if err := json.Unmarshal(list.Body.Bytes(), &records); err != nil {
		t.Fatalf("decoding list error = %v, want nil", err)
	}
	if len(records) != 1 {
		t.Errorf("list returned %d sandboxes, want 1", len(records))
	}
}

// The row is committed before the runtime is asked for anything, so a create
// that fails has to be recorded on it. A sandbox left in pending is
// indistinguishable from one still starting.
func TestCreateSandboxMarksTheSandboxFailedWhenTheRuntimeFails(t *testing.T) {
	st := newTestStore(t)

	api := newTestAPIWithRuntime(t, st, runtimetest.Fake{
		CreateFn: func(context.Context, string, runtime.Spec) (runtime.Info, error) {
			return runtime.Info{}, errors.New("no such image")
		},
	})

	rec := do(t, api, http.MethodPost, "/v1/sandboxes", alpineSpec, withKey("k1"))
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusInternalServerError, rec.Body.String())
	}

	sandboxes, err := st.GetSandboxes(context.Background())
	if err != nil {
		t.Fatalf("GetSandboxes() error = %v, want nil", err)
	}
	if len(sandboxes) != 1 {
		t.Fatalf("GetSandboxes() returned %d sandboxes, want 1", len(sandboxes))
	}
	if sandboxes[0].State != store.Failed {
		t.Errorf("State = %q, want %q", sandboxes[0].State, store.Failed)
	}
}

func TestCreateSandboxMapsStoreFailureTo500(t *testing.T) {
	boom := errors.New("database is on fire")
	api := newTestAPIWithStore(t, &fakeStore{
		createSandbox: func(context.Context, string, store.SpecFile) (*store.Sandbox, error) {
			return nil, boom
		},
	})

	rec := do(t, api, http.MethodPost, "/v1/sandboxes", alpineSpec, withKey("k1"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	got := decodeError(t, rec)
	if got.Error.Code != CodeInternal {
		t.Errorf("error code = %q, want %q", got.Error.Code, CodeInternal)
	}
	// The operator reads the cause in the log; the client reads prose. Leaking
	// the chain tells an attacker what the backend is.
	if strings.Contains(got.Error.Message, boom.Error()) {
		t.Errorf("error message %q leaks the internal error", got.Error.Message)
	}
}

func TestListSandboxesReturnsEmptyArray(t *testing.T) {
	api := newTestAPI(t)

	rec := do(t, api, http.MethodGet, "/v1/sandboxes", "", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	// A nil slice marshals to null, which every client has to special-case.
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Errorf("body = %s, want []", body)
	}
}

func TestListSandboxesReturnsEveryRecord(t *testing.T) {
	api := newTestAPI(t)

	first := mustCreate(t, api, "k1", alpineSpec)
	second := mustCreate(t, api, "k2", `{"image":"debian:12"}`)

	rec := do(t, api, http.MethodGet, "/v1/sandboxes", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	var records []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &records); err != nil {
		t.Fatalf("decoding list %q error = %v, want nil", rec.Body.String(), err)
	}
	if len(records) != 2 {
		t.Fatalf("list returned %d sandboxes, want 2", len(records))
	}

	found := map[string]bool{}
	for _, record := range records {
		found[sandboxID(t, record)] = true
		if _, ok := record["id"]; ok {
			t.Errorf("list record %#v contains the row id, want it omitted", record)
		}
	}
	for _, want := range []string{sandboxID(t, first), sandboxID(t, second)} {
		if !found[want] {
			t.Errorf("list is missing sandbox %q", want)
		}
	}
}

func TestListSandboxesMapsStoreFailureTo500(t *testing.T) {
	api := newTestAPIWithStore(t, &fakeStore{
		getSandboxes: func(context.Context) ([]*store.Sandbox, error) {
			return nil, errors.New("database is on fire")
		},
	})

	rec := do(t, api, http.MethodGet, "/v1/sandboxes", "", nil)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if got := decodeError(t, rec); got.Error.Code != CodeInternal {
		t.Errorf("error code = %q, want %q", got.Error.Code, CodeInternal)
	}
}

// Inspect reports the runtime's view of a running sandbox, so the body is the
// container info, not the store row.
func TestInspectSandboxReturnsRuntimeInfo(t *testing.T) {
	api := newTestAPI(t)
	created := mustCreate(t, api, "k1", alpineSpec)
	id := sandboxID(t, created)

	rec := do(t, api, http.MethodGet, "/v1/sandboxes/"+id, "", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	got := decodeObject(t, rec)
	if got["id"] != id {
		t.Errorf("id = %v, want %q", got["id"], id)
	}
	if got["state"] != string(runtime.StateRunning) {
		t.Errorf("state = %v, want %q", got["state"], runtime.StateRunning)
	}
}

// The id exists but the sandbox has no container yet, so the honest answer is
// a conflict, not the 404 the runtime would report.
func TestInspectPendingSandboxReturns409(t *testing.T) {
	api := newTestAPIWithStore(t, &fakeStore{
		getSandbox: func(context.Context, string) (*store.Sandbox, error) {
			return &store.Sandbox{State: store.Pending}, nil
		},
	})

	rec := do(t, api, http.MethodGet, "/v1/sandboxes/sbx_pending", "", nil)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if got := decodeError(t, rec); got.Error.Code != CodeConflict {
		t.Errorf("error code = %q, want %q", got.Error.Code, CodeConflict)
	}
}

// store.ErrNotFound is the store's sentinel, not httpapi's; the handler is the
// one place allowed to translate it into a status.
func TestInspectUnknownSandboxReturns404(t *testing.T) {
	api := newTestAPI(t)

	rec := do(t, api, http.MethodGet, "/v1/sandboxes/sbx_00000000-0000-0000-0000-000000000000", "", nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if got := decodeError(t, rec); got.Error.Code != CodeNotFound {
		t.Errorf("error code = %q, want %q", got.Error.Code, CodeNotFound)
	}
}

// A malformed id must not read as a database outage.
func TestInspectMalformedSandboxIDReturns404(t *testing.T) {
	api := newTestAPI(t)

	rec := do(t, api, http.MethodGet, "/v1/sandboxes/not-an-id", "", nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestInspectSandboxMapsStoreFailureTo500(t *testing.T) {
	api := newTestAPIWithStore(t, &fakeStore{
		getSandbox: func(context.Context, string) (*store.Sandbox, error) {
			return nil, errors.New("database is on fire")
		},
	})

	rec := do(t, api, http.MethodGet, "/v1/sandboxes/sbx_whatever", "", nil)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if got := decodeError(t, rec); got.Error.Code != CodeInternal {
		t.Errorf("error code = %q, want %q", got.Error.Code, CodeInternal)
	}
}

func TestDestroySandboxReturns204WithNoBody(t *testing.T) {
	api := newTestAPI(t)
	id := sandboxID(t, mustCreate(t, api, "k1", alpineSpec))

	rec := do(t, api, http.MethodDelete, "/v1/sandboxes/"+id, "", nil)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusNoContent, rec.Body.String())
	}
	if body := rec.Body.String(); body != "" {
		t.Errorf("body = %q, want empty: 204 forbids one", body)
	}
}

// "Already gone" is the outcome the caller asked for, so it is a success. The
// second DELETE must not depend on the first having happened in this process.
func TestDeleteIsIdempotent(t *testing.T) {
	api := newTestAPI(t)
	id := sandboxID(t, mustCreate(t, api, "k1", alpineSpec))

	for i := range 2 {
		rec := do(t, api, http.MethodDelete, "/v1/sandboxes/"+id, "", nil)
		if rec.Code != http.StatusNoContent {
			t.Fatalf("DELETE #%d status = %d, want %d (body %s)", i+1, rec.Code, http.StatusNoContent, rec.Body.String())
		}
	}
}

func TestDestroyUnknownSandboxReturns204(t *testing.T) {
	api := newTestAPI(t)

	rec := do(t, api, http.MethodDelete, "/v1/sandboxes/sbx_00000000-0000-0000-0000-000000000000", "", nil)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusNoContent, rec.Body.String())
	}
}

// The record survives the destroy: a deleted sandbox is a terminal row, not a
// missing one, so billing and plan 06's reconciler can still see it. The list
// endpoint is the store's view; inspect-by-id needs a running container.
func TestDestroyedSandboxRemainsListed(t *testing.T) {
	api := newTestAPI(t)
	id := sandboxID(t, mustCreate(t, api, "k1", alpineSpec))

	if rec := do(t, api, http.MethodDelete, "/v1/sandboxes/"+id, "", nil); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE status = %d, want %d", rec.Code, http.StatusNoContent)
	}

	rec := do(t, api, http.MethodGet, "/v1/sandboxes", "", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET after DELETE status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}

	var records []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &records); err != nil {
		t.Fatalf("decoding list %q error = %v, want nil", rec.Body.String(), err)
	}
	if len(records) != 1 {
		t.Fatalf("list returned %d sandboxes after DELETE, want 1", len(records))
	}
	if sandboxID(t, records[0]) != id {
		t.Errorf("listed sandbox_id = %q, want %q", sandboxID(t, records[0]), id)
	}
	if state := records[0]["state"]; state != string(store.Stopped) {
		t.Errorf("state after DELETE = %v, want %q", state, store.Stopped)
	}
}

// runningStore answers every GetSandbox with a running sandbox, for cases that
// are about the runtime call behind the gate.
func runningStore() *fakeStore {
	return &fakeStore{
		getSandbox: func(context.Context, string) (*store.Sandbox, error) {
			return &store.Sandbox{State: store.Running}, nil
		},
	}
}

// The runtime's client-attributable sentinels are wire contract: they must
// classify to their own statuses, not surface as opaque 500s.
func TestRuntimeSentinelsMapToClientStatuses(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		rt     runtimetest.Fake
		status int
		code   string
	}{
		{
			name:   "exec timeout is 504",
			method: http.MethodPost,
			path:   "/v1/sandboxes/sbx_x/exec",
			body:   `{"command":["sleep","999"]}`,
			rt: runtimetest.Fake{
				ExecFn: func(context.Context, string, []string, runtime.ExecOpts) (runtime.ExecResult, error) {
					return runtime.ExecResult{}, runtime.ErrExecTimeout
				},
			},
			status: http.StatusGatewayTimeout,
			code:   CodeTimeout,
		},
		{
			name:   "missing file is 404",
			method: http.MethodGet,
			path:   "/v1/sandboxes/sbx_x/files?path=/nope",
			rt: runtimetest.Fake{
				ReadFileFn: func(context.Context, string, string) ([]byte, error) {
					return nil, runtime.ErrPathNotFound
				},
			},
			status: http.StatusNotFound,
			code:   CodeNotFound,
		},
		{
			name:   "oversized file is 413",
			method: http.MethodPut,
			path:   "/v1/sandboxes/sbx_x/files",
			body:   `{"path":"/f","content":"aGk=","fileMode":420}`,
			rt: runtimetest.Fake{
				WriteFileFn: func(context.Context, string, string, []byte, fs.FileMode) error {
					return runtime.ErrFileTooLarge
				},
			},
			status: http.StatusRequestEntityTooLarge,
			code:   CodeTooLarge,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			api := newTestAPIWithRuntime(t, runningStore(), tt.rt)

			rec := do(t, api, tt.method, tt.path, tt.body, nil)

			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.status, rec.Body.String())
			}
			if got := decodeError(t, rec); got.Error.Code != tt.code {
				t.Errorf("error code = %q, want %q", got.Error.Code, tt.code)
			}
		})
	}
}

// path travels as a query parameter because GET bodies have undefined
// semantics and proxies may drop them. An absent path fails before the store
// is consulted — the nil fakeStore methods would panic otherwise.
func TestFileRoutesRequirePathQueryParam(t *testing.T) {
	api := newTestAPIWithStore(t, &fakeStore{})

	// DELETE on files carries the path the same way, so it is gated the same way.
	for _, tt := range []struct{ method, path string }{
		{http.MethodGet, "/v1/sandboxes/sbx_x/files"},
		{http.MethodGet, "/v1/sandboxes/sbx_x/dir"},
		{http.MethodDelete, "/v1/sandboxes/sbx_x/files"},
	} {
		rec := do(t, api, tt.method, tt.path, "", nil)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s %s = %d, want %d (body %s)", tt.method, tt.path, rec.Code, http.StatusBadRequest, rec.Body.String())
		}
		if got := decodeError(t, rec); got.Error.Code != CodeInvalidRequest {
			t.Errorf("%s %s error code = %q, want %q", tt.method, tt.path, got.Error.Code, CodeInvalidRequest)
		}
	}
}

// Unlike DELETE on a sandbox, removing a path is not idempotent: the caller
// named one specific file, and the runtime is the thing that knows it is gone.
func TestRemovePathReportsAMissingPath(t *testing.T) {
	api := newTestAPIWithRuntime(t, runningStore(), runtimetest.Fake{
		RemovePathFn: func(context.Context, string, string) error {
			return runtime.ErrPathNotFound
		},
	})

	rec := do(t, api, http.MethodDelete, "/v1/sandboxes/sbx_x/files?path=/nope", "", nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if got := decodeError(t, rec); got.Error.Code != CodeNotFound {
		t.Errorf("error code = %q, want %q", got.Error.Code, CodeNotFound)
	}
}

func TestRemovePathAnswers204(t *testing.T) {
	var gotPath string
	api := newTestAPIWithRuntime(t, runningStore(), runtimetest.Fake{
		RemovePathFn: func(_ context.Context, _, path string) error {
			gotPath = path
			return nil
		},
	})

	rec := do(t, api, http.MethodDelete, "/v1/sandboxes/sbx_x/files?path=/work/build", "", nil)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusNoContent, rec.Body.String())
	}
	if gotPath != "/work/build" {
		t.Errorf("removed path = %q, want /work/build", gotPath)
	}
	if rec.Body.Len() != 0 {
		t.Errorf("body = %q, want no body on a 204", rec.Body.String())
	}
}

func TestOversizedRequestBodyReturns413(t *testing.T) {
	api := newTestAPIWithStore(t, &fakeStore{})

	body := `{"path":"/f","content":"` + strings.Repeat("A", maxRequestBytes) + `"}`
	rec := do(t, api, http.MethodPut, "/v1/sandboxes/sbx_x/files", body, nil)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
	if got := decodeError(t, rec); got.Error.Code != CodeTooLarge {
		t.Errorf("error code = %q, want %q", got.Error.Code, CodeTooLarge)
	}
}

func TestRoutingRejectsUnknownPathsAndMethods(t *testing.T) {
	api := newTestAPI(t)

	tests := []struct {
		name   string
		method string
		path   string
		status int
	}{
		{name: "unknown path", method: http.MethodGet, path: "/v1/nope", status: http.StatusNotFound},
		{name: "unversioned path", method: http.MethodGet, path: "/sandboxes", status: http.StatusNotFound},
		{name: "wrong method on collection", method: http.MethodPut, path: "/v1/sandboxes", status: http.StatusMethodNotAllowed},
		{name: "wrong method on item", method: http.MethodPost, path: "/v1/sandboxes/sbx_x", status: http.StatusMethodNotAllowed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rec := do(t, api, tt.method, tt.path, "", nil); rec.Code != tt.status {
				t.Errorf("%s %s = %d, want %d", tt.method, tt.path, rec.Code, tt.status)
			}
		})
	}
}
