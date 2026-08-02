package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/nickstrad/quickspin/internal/api"
	"github.com/nickstrad/quickspin/internal/sandbox"
)

func TestKeepaliveExtendsTTLUpToCap(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		extend time.Duration
	}{
		{name: "empty body uses the default", extend: sandbox.DefaultTTL},
		{name: "explicit extension", body: `{"ttl_seconds":1800}`, extend: 30 * time.Minute},
		{name: "extension above the cap is clamped", body: `{"ttl_seconds":86401}`, extend: sandbox.MaxTTL},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newTestAPI(t)
			id := sandboxID(t, mustCreate(t, srv, "keepalive-"+tt.name, alpineSpec))

			started := time.Now()
			rec := do(t, srv, http.MethodPost, "/v1/sandboxes/"+id+"/keepalive", tt.body, nil)
			finished := time.Now()
			if rec.Code != http.StatusOK {
				t.Fatalf("POST keepalive = %d, want %d (body %s)", rec.Code, http.StatusOK, rec.Body.String())
			}

			var response api.SandboxResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
				t.Fatalf("decoding keepalive response %q error = %v, want nil", rec.Body.String(), err)
			}
			if response.ExpiresAt.Before(started.Add(tt.extend)) || response.ExpiresAt.After(finished.Add(tt.extend)) {
				t.Errorf("response ExpiresAt = %v, want between %v and %v",
					response.ExpiresAt, started.Add(tt.extend), finished.Add(tt.extend))
			}

			stored := mustGetSandbox(t, srv, id)
			if !stored.ExpiresAt.Equal(response.ExpiresAt) {
				t.Errorf("stored ExpiresAt = %v, want response value %v", stored.ExpiresAt, response.ExpiresAt)
			}
		})
	}
}

func TestKeepaliveRejectsNegativeExtensionWithoutChangingExpiry(t *testing.T) {
	srv := newTestAPI(t)
	id := sandboxID(t, mustCreate(t, srv, "negative-keepalive", alpineSpec))
	before := mustGetSandbox(t, srv, id)

	rec := do(t, srv, http.MethodPost, "/v1/sandboxes/"+id+"/keepalive", `{"ttl_seconds":-1}`, nil)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("POST keepalive with negative ttl = %d, want %d (body %s)", rec.Code, http.StatusUnprocessableEntity, rec.Body.String())
	}

	after := mustGetSandbox(t, srv, id)
	if !after.ExpiresAt.Equal(before.ExpiresAt) {
		t.Errorf("ExpiresAt after rejection = %v, want unchanged %v", after.ExpiresAt, before.ExpiresAt)
	}
}

func TestKeepaliveRejectsSandboxesAlreadyBeingDestroyed(t *testing.T) {
	srv := newTestAPI(t)
	id := sandboxID(t, mustCreate(t, srv, "stopping-keepalive", alpineSpec))
	transitionSandbox(t, srv, id, sandbox.Pending, sandbox.Running)
	transitionSandbox(t, srv, id, sandbox.Running, sandbox.Stopping)

	rec := do(t, srv, http.MethodPost, "/v1/sandboxes/"+id+"/keepalive", "", nil)
	if rec.Code != http.StatusConflict {
		t.Fatalf("POST keepalive for stopping sandbox = %d, want %d (body %s)", rec.Code, http.StatusConflict, rec.Body.String())
	}
}

func TestKeepaliveForUnknownSandboxReturnsNotFound(t *testing.T) {
	srv := newTestAPI(t)
	rec := do(t, srv, http.MethodPost,
		"/v1/sandboxes/sbx_00000000-0000-0000-0000-000000000000/keepalive", "", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("POST keepalive for unknown sandbox = %d, want %d (body %s)", rec.Code, http.StatusNotFound, rec.Body.String())
	}
}
