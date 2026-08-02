package client_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/nickstrad/quickspin/internal/api"
	"github.com/nickstrad/quickspin/internal/client"
	"github.com/nickstrad/quickspin/internal/events"
	"github.com/nickstrad/quickspin/internal/sandbox"
)

type observedRequest struct {
	method string
	path   string
	body   []byte
}

func TestSandboxEventsRoundTrip(t *testing.T) {
	c, st := newTestClient(t, okRuntime())

	created, err := c.CreateSandbox(t.Context(), "events-key", sandbox.SpecFile{}, time.Minute)
	if err != nil {
		t.Fatalf("CreateSandbox error = %v, want nil", err)
	}
	if _, err := st.UpdateSandboxState(t.Context(), created.SandboxID, sandbox.Pending, sandbox.Running, "container observed running"); err != nil {
		t.Fatalf("UpdateSandboxState(pending, running) error = %v, want nil", err)
	}

	got, err := c.GetSandboxEvents(t.Context(), created.SandboxID)
	if err != nil {
		t.Fatalf("GetSandboxEvents error = %v, want nil", err)
	}
	if len(got) != 2 {
		t.Fatalf("GetSandboxEvents returned %d events, want create plus transition", len(got))
	}

	assertEvent(t, got[0], created.SandboxID, "", sandbox.Pending, "sandbox record created")
	assertEvent(t, got[1], created.SandboxID, sandbox.Pending, sandbox.Running, "container observed running")
	if got[0].At.IsZero() || got[1].At.IsZero() || got[1].At.Before(got[0].At) {
		t.Errorf("event times = %s then %s, want non-zero chronological timestamps", got[0].At, got[1].At)
	}
}

func TestKeepaliveSandboxRoundTrip(t *testing.T) {
	c, _ := newTestClient(t, okRuntime())

	created, err := c.CreateSandbox(t.Context(), "keepalive-key", sandbox.SpecFile{}, time.Minute)
	if err != nil {
		t.Fatalf("CreateSandbox error = %v, want nil", err)
	}

	before := time.Now()
	got, err := c.KeepaliveSandbox(t.Context(), created.SandboxID, 2*time.Hour)
	after := time.Now()
	if err != nil {
		t.Fatalf("KeepaliveSandbox error = %v, want nil", err)
	}
	if got.SandboxID != created.SandboxID || got.State != created.State {
		t.Errorf("KeepaliveSandbox returned (%q, %q), want (%q, %q)",
			got.SandboxID, got.State, created.SandboxID, created.State)
	}
	if got.ExpiresAt.Before(before.Add(2*time.Hour)) || got.ExpiresAt.After(after.Add(2*time.Hour)) {
		t.Errorf("ExpiresAt = %s, want server request time + 2h (between %s and %s)",
			got.ExpiresAt, before.Add(2*time.Hour), after.Add(2*time.Hour))
	}
}

func assertEvent(t *testing.T, got *events.Event, sandboxID string, from, to sandbox.TaskState, reason string) {
	t.Helper()
	if got == nil {
		t.Fatal("event = nil, want a decoded event")
	}
	if got.SandboxID != sandboxID || got.FromState != from || got.ToState != to || got.Reason != reason {
		t.Errorf("event = %+v, want sandbox %q, %q -> %q, reason %q", got, sandboxID, from, to, reason)
	}
}

// The integrated tests above use valid generated IDs. This wire-level test
// covers the boundary they cannot reach: a caller-supplied ID must stay one
// escaped path segment, and even a zero keepalive must carry a JSON body.
func TestLeaseAndEventRequestsCrossTheWire(t *testing.T) {
	requests := make(chan observedRequest, 1)

	expiresAt := time.Date(2026, time.August, 1, 12, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests <- observedRequest{method: r.Method, path: r.URL.EscapedPath(), body: body}

		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode([]map[string]any{{
				"sandbox_id": "sbx_name/with?segments#part",
				"from_state": "pending",
				"to_state":   "running",
				"at":         expiresAt,
				"reason":     "container observed running",
			}})
			return
		}
		_ = api.WriteJSON(w, http.StatusOK, api.SandboxResponse{
			SandboxID: "sbx_name/with?segments#part",
			State:     string(sandbox.Pending),
			ExpiresAt: expiresAt,
		})
	}))
	t.Cleanup(server.Close)

	c := client.New(server.URL, server.Client())
	const id = "sbx_name/with?segments#part"

	evts, err := c.GetSandboxEvents(t.Context(), id)
	if err != nil {
		t.Fatalf("GetSandboxEvents error = %v, want nil", err)
	}
	if len(evts) != 1 || evts[0].SandboxID != id || !evts[0].At.Equal(expiresAt) {
		t.Errorf("GetSandboxEvents = %+v, want the wire event intact", evts)
	}
	assertRequest(t, <-requests, http.MethodGet,
		"/v1/sandboxes/sbx_name%2Fwith%3Fsegments%23part/events", nil)

	for _, tt := range []struct {
		name string
		ttl  time.Duration
		body []byte
	}{
		{name: "zero still sends an object", body: []byte(`{}`)},
		{name: "negative reaches the server", ttl: -time.Second, body: []byte(`{"ttl_seconds":-1}`)},
		{name: "negative subsecond is not omitted", ttl: -time.Millisecond, body: []byte(`{"ttl_seconds":-1}`)},
		{name: "positive uses seconds", ttl: 90 * time.Second, body: []byte(`{"ttl_seconds":90}`)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := c.KeepaliveSandbox(t.Context(), id, tt.ttl)
			if err != nil {
				t.Fatalf("KeepaliveSandbox error = %v, want nil", err)
			}
			if got.SandboxID != id || got.State != sandbox.Pending || !got.ExpiresAt.Equal(expiresAt) {
				t.Errorf("KeepaliveSandbox = %+v, want the response fields intact", got)
			}
			assertRequest(t, <-requests, http.MethodPost,
				"/v1/sandboxes/sbx_name%2Fwith%3Fsegments%23part/keepalive", tt.body)
		})
	}
}

func assertRequest(t *testing.T, got observedRequest, method, path string, body []byte) {
	t.Helper()
	if got.method != method || got.path != path || !bytes.Equal(got.body, body) {
		t.Errorf("request = %s %s body %q, want %s %s body %q",
			got.method, got.path, got.body, method, path, body)
	}
}
