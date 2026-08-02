package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/quickspin/internal/api"
	"github.com/nickstrad/quickspin/internal/events"
	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/nickstrad/quickspin/internal/runtime/runtimetest"
	"github.com/nickstrad/quickspin/internal/sandbox"
	"github.com/nickstrad/quickspin/internal/store"
	"github.com/nickstrad/quickspin/internal/store/sqlite"
	"github.com/nickstrad/quickspin/internal/store/storetest"
)

func newTestAPI(t *testing.T) *API {
	t.Helper()
	return newTestAPIWithStore(t, newTestStore(t))
}

func newTestStore(t *testing.T) *sqlite.Store {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := sqlite.New(context.Background(), ":memory:", "", logger)
	if err != nil {
		t.Fatalf("sqlite.New(:memory:) error = %v, want nil", err)
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

	// Inspect and Destroy are scripted so lifecycle tests can walk create →
	// inspect → delete without a daemon. Create is not: no route starts a
	// container, so an unset CreateFn panics a handler that tries.
	return newTestAPIWithRuntime(t, st, runtimetest.Fake{
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

	srv := NewAPI("127.0.0.1", 0, slog.New(slog.NewTextHandler(io.Discard, nil)), st, rt)
	srv.Handler()
	return &srv
}

func do(t *testing.T, srv *API, method, path, body string, headers map[string]string) *httptest.ResponseRecorder {
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
	srv.Router.ServeHTTP(rec, req)
	return rec
}

func withKey(key string) map[string]string {
	return map[string]string{api.IdempotencyKeyHeader: key}
}

// mustCreate returns the decoded record so later requests can address the
// sandbox by the id the API itself handed out.
func mustCreate(t *testing.T, srv *API, key, body string) map[string]any {
	t.Helper()

	rec := do(t, srv, http.MethodPost, "/v1/sandboxes", body, withKey(key))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("POST /v1/sandboxes = %d, want %d (body %s)", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	return decodeObject(t, rec)
}

// markRunning stands in for the reconciler, which is the only thing that starts
// a container and moves the row out of pending.
func markRunning(t *testing.T, srv *API, id string) {
	t.Helper()
	transitionSandbox(t, srv, id, sandbox.Pending, sandbox.Running)
}

func transitionSandbox(t *testing.T, srv *API, id string, from, to sandbox.TaskState) *sandbox.Sandbox {
	t.Helper()

	updated, err := srv.store.UpdateSandboxState(t.Context(), id, from, to, "test")
	if err != nil {
		t.Fatalf("UpdateSandboxState(%s, %s, %s) error = %v, want nil", id, from, to, err)
	}
	return updated
}

func mustGetSandbox(t *testing.T, srv *API, id string) *sandbox.Sandbox {
	t.Helper()

	sbx, err := srv.store.GetSandbox(t.Context(), id)
	if err != nil {
		t.Fatalf("GetSandbox(%s) error = %v, want nil", id, err)
	}
	return sbx
}

func decodeObject(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()

	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding response %q error = %v, want nil", rec.Body.String(), err)
	}
	return got
}

func decodeError(t *testing.T, rec *httptest.ResponseRecorder) api.ErrorResponse {
	t.Helper()

	var got api.ErrorResponse
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

const alpineSpec = `{"spec":{"image":"alpine:3.20"}}`

// A bind failure used to be discarded inside a goroutine: the process logged
// that it was listening and then blocked forever on a port it never held.
func TestStartReturnsTheBindError(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port error = %v, want nil", err)
	}
	t.Cleanup(func() { held.Close() })
	port := held.Addr().(*net.TCPAddr).Port

	srv := NewAPI("127.0.0.1", port, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)

	// Unbuffered and never closed: Start must return without it, so a Start that
	// still waits for a shutdown signal hangs the test rather than passing.
	if err := srv.Start(make(chan struct{})); err == nil {
		t.Fatal("Start() error = nil, want the address-in-use failure")
	}
}

// Stop on an API that never started is the path a failed Start leaves behind.
func TestStopBeforeStartIsNotAnError(t *testing.T) {
	srv := NewAPI("127.0.0.1", 0, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)

	if err := srv.Stop(); err != nil {
		t.Errorf("Stop() error = %v, want nil", err)
	}
}

// The request records intent and returns; the container is the reconciler's
// job. The Fake panics on an unset CreateFn, so a handler that still starts a
// runtime fails here.
func TestCreateSandboxAcceptsAndReturnsAPendingRecord(t *testing.T) {
	srv := newTestAPIWithRuntime(t, newTestStore(t), runtimetest.Fake{})

	rec := do(t, srv, http.MethodPost, "/v1/sandboxes", alpineSpec, withKey("k1"))

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	got := decodeObject(t, rec)
	if id := sandboxID(t, got); !strings.HasPrefix(id, "sbx_") {
		t.Errorf("sandbox_id = %q, want an sbx_-prefixed id", id)
	}
	if got["state"] != string(sandbox.Pending) {
		t.Errorf("state = %v, want %q", got["state"], sandbox.Pending)
	}
	spec, ok := got["spec"].(map[string]any)
	if !ok || spec["image"] != "alpine:3.20" {
		t.Errorf("spec = %#v, want the submitted image echoed back", got["spec"])
	}
}

// Stored specs remain unresolved so defaults can change independently — the
// reconciler resolves them when it builds the container.
func TestCreateSandboxStoresTheSpecUnresolved(t *testing.T) {
	srv := newTestAPIWithRuntime(t, newTestStore(t), runtimetest.Fake{})

	record := mustCreate(t, srv, "k1", `{"spec":{}}`)

	spec, ok := record["spec"].(map[string]any)
	if !ok || spec["image"] != nil {
		t.Errorf("echoed spec = %#v, want a null image", spec)
	}
}

func TestCreateSandboxAppliesTheRequestedTTL(t *testing.T) {
	tests := []struct {
		name string
		body string
		want time.Duration
	}{
		{name: "explicit", body: `{"spec":{"image":"alpine:3.20"},"ttl_seconds":60}`, want: time.Minute},
		{name: "omitted", body: alpineSpec, want: sandbox.DefaultTTL},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestAPI(t)

			before := time.Now()
			record := mustCreate(t, srv, "k1", tt.body)
			expiresAt, err := time.Parse(time.RFC3339Nano, record["expires_at"].(string))
			if err != nil {
				t.Fatalf("parsing expires_at %v error = %v, want nil", record["expires_at"], err)
			}

			if expiresAt.Before(before.Add(tt.want)) || expiresAt.After(time.Now().Add(tt.want)) {
				t.Errorf("expires_at = %v, want roughly %v after the request", expiresAt, tt.want)
			}
		})
	}
}

func TestCreateSandboxRejectsAnOverCapTTL(t *testing.T) {
	srv := newTestAPI(t)

	body := fmt.Sprintf(`{"spec":{},"ttl_seconds":%d}`, int64(sandbox.MaxTTL/time.Second)+1)
	rec := do(t, srv, http.MethodPost, "/v1/sandboxes", body, withKey("k1"))

	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusUnprocessableEntity, rec.Body.String())
	}
	if got := decodeError(t, rec); got.Error.Code != api.CodeUnprocessable {
		t.Errorf("error code = %q, want %q", got.Error.Code, api.CodeUnprocessable)
	}
}

// The autoincrement row id is an internal key; a client that learns it can
// enumerate every sandbox on the host.
func TestCreateSandboxDoesNotLeakRowID(t *testing.T) {
	srv := newTestAPI(t)

	got := mustCreate(t, srv, "k1", alpineSpec)

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
			code:   api.CodeInvalidRequest,
		},
		{
			name:   "empty body",
			key:    "k1",
			body:   "",
			status: http.StatusBadRequest,
			code:   api.CodeInvalidRequest,
		},
		{
			name:   "malformed json",
			key:    "k1",
			body:   `{"image":`,
			status: http.StatusBadRequest,
			code:   api.CodeInvalidRequest,
		},
		{
			name:   "unknown field",
			key:    "k1",
			body:   `{"spec":{"image":"alpine:3.20"},"gpus":4}`,
			status: http.StatusBadRequest,
			code:   api.CodeInvalidRequest,
		},
		{
			name:   "unrecognized keys only",
			key:    "k1",
			body:   `{"gpus":4}`,
			status: http.StatusBadRequest,
			code:   api.CodeInvalidRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestAPI(t)

			headers := map[string]string{}
			if tt.key != "" {
				headers[api.IdempotencyKeyHeader] = tt.key
			}
			rec := do(t, srv, http.MethodPost, "/v1/sandboxes", tt.body, headers)

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
	srv := newTestAPI(t)

	first := mustCreate(t, srv, "same-operation", alpineSpec)
	second := mustCreate(t, srv, "same-operation", `{"spec":{"image":"debian:12"}}`)

	if sandboxID(t, second) != sandboxID(t, first) {
		t.Errorf("retry sandbox_id = %q, want the original %q", sandboxID(t, second), sandboxID(t, first))
	}

	// The key deduplicates the side effect, not the request: the second body is
	// ignored and the caller sees the original sandbox as it is now.
	spec := second["spec"].(map[string]any)
	if spec["image"] != "alpine:3.20" {
		t.Errorf("retry spec.image = %v, want the original alpine:3.20", spec["image"])
	}

	list := do(t, srv, http.MethodGet, "/v1/sandboxes", "", nil)
	var records []map[string]any
	if err := json.Unmarshal(list.Body.Bytes(), &records); err != nil {
		t.Fatalf("decoding list error = %v, want nil", err)
	}
	if len(records) != 1 {
		t.Errorf("list returned %d sandboxes, want 1", len(records))
	}
}

func TestCreateSandboxMapsStoreFailureTo500(t *testing.T) {
	boom := errors.New("database is on fire")
	srv := newTestAPIWithStore(t, storetest.Fake{
		CreateSandboxFn: func(context.Context, string, sandbox.SpecFile, time.Time) (*sandbox.Sandbox, error) {
			return nil, boom
		},
	})

	rec := do(t, srv, http.MethodPost, "/v1/sandboxes", alpineSpec, withKey("k1"))

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	got := decodeError(t, rec)
	if got.Error.Code != api.CodeInternal {
		t.Errorf("error code = %q, want %q", got.Error.Code, api.CodeInternal)
	}
	// The operator reads the cause in the log; the client reads prose. Leaking
	// the chain tells an attacker what the backend is.
	if strings.Contains(got.Error.Message, boom.Error()) {
		t.Errorf("error message %q leaks the internal error", got.Error.Message)
	}
}

func TestListSandboxesReturnsEmptyArray(t *testing.T) {
	srv := newTestAPI(t)

	rec := do(t, srv, http.MethodGet, "/v1/sandboxes", "", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	// A nil slice marshals to null, which every client has to special-case.
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Errorf("body = %s, want []", body)
	}
}

func TestListSandboxesReturnsEveryRecord(t *testing.T) {
	srv := newTestAPI(t)

	first := mustCreate(t, srv, "k1", alpineSpec)
	second := mustCreate(t, srv, "k2", `{"spec":{"image":"debian:12"}}`)

	rec := do(t, srv, http.MethodGet, "/v1/sandboxes", "", nil)
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
	srv := newTestAPIWithStore(t, storetest.Fake{
		GetSandboxesFn: func(context.Context) ([]*sandbox.Sandbox, error) {
			return nil, errors.New("database is on fire")
		},
	})

	rec := do(t, srv, http.MethodGet, "/v1/sandboxes", "", nil)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if got := decodeError(t, rec); got.Error.Code != api.CodeInternal {
		t.Errorf("error code = %q, want %q", got.Error.Code, api.CodeInternal)
	}
}

func TestGetSandboxEventsReturnsOrderedWireEvents(t *testing.T) {
	srv := newTestAPI(t)
	id := sandboxID(t, mustCreate(t, srv, "k1", alpineSpec))
	markRunning(t, srv, id)

	rec := do(t, srv, http.MethodGet, "/v1/sandboxes/"+id+"/events", "", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	var got []struct {
		api.SandboxEventResponse
		ID json.RawMessage `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding events %q error = %v, want nil", rec.Body.String(), err)
	}
	if len(got) != 2 {
		t.Fatalf("event count = %d, want the create and running transitions", len(got))
	}
	if got[0].FromState != "" || got[0].ToState != string(sandbox.Pending) {
		t.Errorf("first event = %#v, want creation -> pending", got[0])
	}
	if got[1].FromState != string(sandbox.Pending) || got[1].ToState != string(sandbox.Running) {
		t.Errorf("second event = %#v, want pending -> running", got[1])
	}
	for i, event := range got {
		if event.SandboxID != id {
			t.Errorf("event %d sandbox_id = %v, want %q", i, event.SandboxID, id)
		}
		if event.At.IsZero() || event.Reason == "" {
			t.Errorf("event %d = %#v, want its time and reason", i, event)
		}
		if event.ID != nil {
			t.Errorf("event %d = %#v, want the store row id omitted", i, event)
		}
	}
}

func TestGetSandboxEventsReturnsEmptyArray(t *testing.T) {
	srv := newTestAPIWithStore(t, storetest.Fake{
		GetSandboxFn: func(context.Context, string) (*sandbox.Sandbox, error) {
			return &sandbox.Sandbox{SandboxID: "sbx_known"}, nil
		},
		GetSandboxEventsFn: func(context.Context, string) ([]*events.Event, error) {
			return nil, nil
		},
	})

	rec := do(t, srv, http.MethodGet, "/v1/sandboxes/sbx_known/events", "", nil)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
	}
	if body := strings.TrimSpace(rec.Body.String()); body != "[]" {
		t.Errorf("body = %s, want []", body)
	}
}

func TestGetSandboxEventsUnknownSandboxReturns404(t *testing.T) {
	srv := newTestAPIWithStore(t, storetest.Fake{
		GetSandboxFn: func(context.Context, string) (*sandbox.Sandbox, error) {
			return nil, store.ErrNotFound
		},
	})

	rec := do(t, srv, http.MethodGet, "/v1/sandboxes/sbx_missing/events", "", nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if got := decodeError(t, rec); got.Error.Code != api.CodeNotFound {
		t.Errorf("error code = %q, want %q", got.Error.Code, api.CodeNotFound)
	}
}

func TestGetSandboxEventsMapsStoreFailureTo500(t *testing.T) {
	srv := newTestAPIWithStore(t, storetest.Fake{
		GetSandboxFn: func(context.Context, string) (*sandbox.Sandbox, error) {
			return &sandbox.Sandbox{SandboxID: "sbx_known"}, nil
		},
		GetSandboxEventsFn: func(context.Context, string) ([]*events.Event, error) {
			return nil, errors.New("database is on fire")
		},
	})

	rec := do(t, srv, http.MethodGet, "/v1/sandboxes/sbx_known/events", "", nil)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if got := decodeError(t, rec); got.Error.Code != api.CodeInternal {
		t.Errorf("error code = %q, want %q", got.Error.Code, api.CodeInternal)
	}
}

// Inspect reports the runtime's view of a running sandbox, so the body is the
// container info, not the store row.
func TestInspectSandboxReturnsRuntimeInfo(t *testing.T) {
	srv := newTestAPI(t)
	id := sandboxID(t, mustCreate(t, srv, "k1", alpineSpec))
	markRunning(t, srv, id)

	rec := do(t, srv, http.MethodGet, "/v1/sandboxes/"+id, "", nil)

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
	srv := newTestAPIWithStore(t, storetest.Fake{
		GetSandboxFn: func(context.Context, string) (*sandbox.Sandbox, error) {
			return &sandbox.Sandbox{State: sandbox.Pending}, nil
		},
	})

	rec := do(t, srv, http.MethodGet, "/v1/sandboxes/sbx_pending", "", nil)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusConflict, rec.Body.String())
	}
	if got := decodeError(t, rec); got.Error.Code != api.CodeConflict {
		t.Errorf("error code = %q, want %q", got.Error.Code, api.CodeConflict)
	}
}

// store.ErrNotFound is the store's sentinel, not httpapi's; the handler is the
// one place allowed to translate it into a status.
func TestInspectUnknownSandboxReturns404(t *testing.T) {
	srv := newTestAPI(t)

	rec := do(t, srv, http.MethodGet, "/v1/sandboxes/sbx_00000000-0000-0000-0000-000000000000", "", nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if got := decodeError(t, rec); got.Error.Code != api.CodeNotFound {
		t.Errorf("error code = %q, want %q", got.Error.Code, api.CodeNotFound)
	}
}

// A malformed id must not read as a database outage.
func TestInspectMalformedSandboxIDReturns404(t *testing.T) {
	srv := newTestAPI(t)

	rec := do(t, srv, http.MethodGet, "/v1/sandboxes/not-an-id", "", nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}

func TestInspectSandboxMapsStoreFailureTo500(t *testing.T) {
	srv := newTestAPIWithStore(t, storetest.Fake{
		GetSandboxFn: func(context.Context, string) (*sandbox.Sandbox, error) {
			return nil, errors.New("database is on fire")
		},
	})

	rec := do(t, srv, http.MethodGet, "/v1/sandboxes/sbx_whatever", "", nil)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusInternalServerError)
	}
	if got := decodeError(t, rec); got.Error.Code != api.CodeInternal {
		t.Errorf("error code = %q, want %q", got.Error.Code, api.CodeInternal)
	}
}

// The row can say running while the container is gone — routine until the
// reconciler exists. Exec already answered 404 for it; Inspect used to answer
// 500 for the same condition.
func TestInspectSandboxMapsAMissingContainerTo404(t *testing.T) {
	srv := newTestAPIWithRuntime(t, newTestStore(t), runtimetest.Fake{
		InspectFn: func(context.Context, string) (runtime.Info, error) {
			return runtime.Info{}, runtime.E("runtime.Fake.Inspect", "no such container", runtime.ErrNotFound)
		},
	})
	id := sandboxID(t, mustCreate(t, srv, "k1", alpineSpec))
	markRunning(t, srv, id)

	rec := do(t, srv, http.MethodGet, "/v1/sandboxes/"+id, "", nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if got := decodeError(t, rec); got.Error.Code != api.CodeNotFound {
		t.Errorf("error code = %q, want %q", got.Error.Code, api.CodeNotFound)
	}
}

// 202: the handler records the stop intent, and the reconciler is what removes
// the container, so the response cannot claim the sandbox is already gone.
func TestDestroySandboxReturns202WithNoBody(t *testing.T) {
	srv := newTestAPI(t)
	id := sandboxID(t, mustCreate(t, srv, "k1", alpineSpec))

	rec := do(t, srv, http.MethodDelete, "/v1/sandboxes/"+id, "", nil)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusAccepted, rec.Body.String())
	}
	if body := rec.Body.String(); body != "" {
		t.Errorf("body = %q, want empty", body)
	}
}

// "Already gone" is the outcome the caller asked for, so it is a success. The
// second DELETE must not depend on the first having happened in this process.
func TestDeleteIsIdempotent(t *testing.T) {
	srv := newTestAPI(t)
	id := sandboxID(t, mustCreate(t, srv, "k1", alpineSpec))

	for i := range 2 {
		rec := do(t, srv, http.MethodDelete, "/v1/sandboxes/"+id, "", nil)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("DELETE #%d status = %d, want %d (body %s)", i+1, rec.Code, http.StatusAccepted, rec.Body.String())
		}
	}
}

func TestDestroyUnknownSandboxReturns202(t *testing.T) {
	srv := newTestAPI(t)

	rec := do(t, srv, http.MethodDelete, "/v1/sandboxes/sbx_00000000-0000-0000-0000-000000000000", "", nil)

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusAccepted, rec.Body.String())
	}
}

// The record survives the destroy: a deleted sandbox is a stopping row on its
// way to a terminal one, not a missing row, so billing and the reconciler can
// still see it. The list endpoint is the store's view; inspect-by-id needs a
// running container.
func TestDestroyedSandboxRemainsListed(t *testing.T) {
	srv := newTestAPI(t)
	id := sandboxID(t, mustCreate(t, srv, "k1", alpineSpec))
	markRunning(t, srv, id)

	if rec := do(t, srv, http.MethodDelete, "/v1/sandboxes/"+id, "", nil); rec.Code != http.StatusAccepted {
		t.Fatalf("DELETE status = %d, want %d", rec.Code, http.StatusAccepted)
	}

	rec := do(t, srv, http.MethodGet, "/v1/sandboxes", "", nil)
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
	if state := records[0]["state"]; state != string(sandbox.Stopping) {
		t.Errorf("state after DELETE = %v, want %q", state, sandbox.Stopping)
	}
}

// runningStore answers every GetSandbox with a running sandbox, for cases that
// are about the runtime call behind the gate.
func runningStore() storetest.Fake {
	return storetest.Fake{
		GetSandboxFn: func(context.Context, string) (*sandbox.Sandbox, error) {
			return &sandbox.Sandbox{State: sandbox.Running}, nil
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
			code:   api.CodeTimeout,
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
			code:   api.CodeNotFound,
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
			code:   api.CodeTooLarge,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestAPIWithRuntime(t, runningStore(), tt.rt)

			rec := do(t, srv, tt.method, tt.path, tt.body, nil)

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
// is consulted — the Fake's unset methods would panic otherwise.
func TestFileRoutesRequirePathQueryParam(t *testing.T) {
	srv := newTestAPIWithStore(t, storetest.Fake{})

	// DELETE on files carries the path the same way, so it is gated the same way.
	for _, tt := range []struct{ method, path string }{
		{http.MethodGet, "/v1/sandboxes/sbx_x/files"},
		{http.MethodGet, "/v1/sandboxes/sbx_x/dir"},
		{http.MethodDelete, "/v1/sandboxes/sbx_x/files"},
	} {
		rec := do(t, srv, tt.method, tt.path, "", nil)

		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s %s = %d, want %d (body %s)", tt.method, tt.path, rec.Code, http.StatusBadRequest, rec.Body.String())
		}
		if got := decodeError(t, rec); got.Error.Code != api.CodeInvalidRequest {
			t.Errorf("%s %s error code = %q, want %q", tt.method, tt.path, got.Error.Code, api.CodeInvalidRequest)
		}
	}
}

// Unlike DELETE on a sandbox, removing a path is not idempotent: the caller
// named one specific file, and the runtime is the thing that knows it is gone.
func TestRemovePathReportsAMissingPath(t *testing.T) {
	srv := newTestAPIWithRuntime(t, runningStore(), runtimetest.Fake{
		RemovePathFn: func(context.Context, string, string) error {
			return runtime.ErrPathNotFound
		},
	})

	rec := do(t, srv, http.MethodDelete, "/v1/sandboxes/sbx_x/files?path=/nope", "", nil)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusNotFound, rec.Body.String())
	}
	if got := decodeError(t, rec); got.Error.Code != api.CodeNotFound {
		t.Errorf("error code = %q, want %q", got.Error.Code, api.CodeNotFound)
	}
}

func TestRemovePathAnswers204(t *testing.T) {
	var gotPath string
	srv := newTestAPIWithRuntime(t, runningStore(), runtimetest.Fake{
		RemovePathFn: func(_ context.Context, _, path string) error {
			gotPath = path
			return nil
		},
	})

	rec := do(t, srv, http.MethodDelete, "/v1/sandboxes/sbx_x/files?path=/work/build", "", nil)

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
	srv := newTestAPIWithStore(t, storetest.Fake{})

	body := `{"path":"/f","content":"` + strings.Repeat("A", maxRequestBytes) + `"}`
	rec := do(t, srv, http.MethodPut, "/v1/sandboxes/sbx_x/files", body, nil)

	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, http.StatusRequestEntityTooLarge, rec.Body.String())
	}
	if got := decodeError(t, rec); got.Error.Code != api.CodeTooLarge {
		t.Errorf("error code = %q, want %q", got.Error.Code, api.CodeTooLarge)
	}
}

func TestRoutingRejectsUnknownPathsAndMethods(t *testing.T) {
	srv := newTestAPI(t)

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
			if rec := do(t, srv, tt.method, tt.path, "", nil); rec.Code != tt.status {
				t.Errorf("%s %s = %d, want %d", tt.method, tt.path, rec.Code, tt.status)
			}
		})
	}
}
