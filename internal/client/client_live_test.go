package client_test

import (
	"context"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nickstrad/quickspin/internal/client"
	"github.com/nickstrad/quickspin/internal/httpapi"
	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/nickstrad/quickspin/internal/store"
)

// The one place client, httpapi, the SQLite store, and a real Docker daemon meet.
// Every other test in this package scripts runtimetest.Fake, so the handlers'
// agreement with Docker — the pending-to-running transition, a Destroy against a
// container that genuinely exists — is only ever asserted here.
//
// Like internal/runtime's live suite this compiles during an ordinary `make test`
// and skips at run time, so a machine with no Docker still builds every
// assertion below.
const (
	dockerGate = "QUICKSPIN_TEST_DOCKER"
	// Shared with internal/runtime's live suite via hack/test-runtime-docker.sh,
	// so the two cannot drift onto different images. The default is only for a
	// standalone run; it must be an image whose default process stays up, or
	// inspect races the container's own exit.
	imageEnv     = "QUICKSPIN_TEST_IMAGE"
	defaultImage = "docker.io/library/nginx:1.27-alpine"

	// Generous because a cold daemon pays for a pull inside Create.
	liveTimeout = 3 * time.Minute

	// Cleanup cannot use t.Context(): the testing package cancels it just before
	// cleanups run, so a cleanup reaching for it would fail exactly when there
	// was something to clean up.
	teardownTimeout = time.Minute
)

// newLiveClient is newTestClient with the two fakes replaced: a real Docker
// runtime built the way `quickspin serve` builds it, and a file-backed store
// rather than :memory:, so the composition under test is the shipped one.
func newLiveClient(t *testing.T) *client.Client {
	t.Helper()

	if os.Getenv(dockerGate) != "1" {
		t.Skipf("set %s=1 and DOCKER_HOST to a test-owned Docker daemon to run this; `make test-docker` does both",
			dockerGate)
	}

	logger := slog.New(slog.NewTextHandler(liveLogWriter{t}, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// A nil client, so the SDK reads DOCKER_HOST — the same construction
	// internal/cli/serve.go performs.
	rt, err := runtime.NewDockerRuntime(nil, logger)
	if err != nil {
		t.Fatalf("NewDockerRuntime from the environment: %v", err)
	}

	st, err := store.NewSqlliteStore(t.Context(), filepath.Join(t.TempDir(), "quickspin.db"), "", logger)
	if err != nil {
		t.Fatalf("NewSqlliteStore error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := st.Cleanup(); err != nil {
			t.Errorf("Cleanup() error = %v, want nil", err)
		}
	})

	api := httpapi.NewAPI("127.0.0.1", 0, logger, st, rt)
	server := httptest.NewServer(api.Handler())
	t.Cleanup(server.Close)

	return client.New(server.URL, server.Client())
}

func liveImage() string {
	if image := os.Getenv(imageEnv); image != "" {
		return image
	}
	return defaultImage
}

func TestLiveSandboxLifecycle(t *testing.T) {
	c := newLiveClient(t)

	ctx, cancel := context.WithTimeout(t.Context(), liveTimeout)
	defer cancel()

	image := liveImage()
	created, err := c.CreateSandbox(ctx, "live-lifecycle", store.SpecFile{Image: &image})
	if err != nil {
		t.Fatalf("CreateSandbox(%s) error = %v, want nil", image, err)
	}
	// Registered before the first assertion that can fail, so a container is
	// never left behind for the harness's leak check to report as this run's
	// failure for the wrong reason.
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), teardownTimeout)
		defer cancel()
		if err := c.DestroySandbox(ctx, created.SandboxID); err != nil {
			t.Errorf("cleanup DestroySandbox(%s) error = %v, want nil", created.SandboxID, err)
		}
	})

	if created.State != store.Running {
		t.Fatalf("CreateSandbox state = %q, want %q", created.State, store.Running)
	}

	// Inspect answers a runtime.Info keyed on ID, while create and list answer
	// the store's row keyed on SandboxID. The two are allowed to disagree — that
	// is the point of recording intent separately from reality.
	info, err := c.InspectSandbox(ctx, created.SandboxID)
	if err != nil {
		t.Fatalf("InspectSandbox(%s) error = %v, want nil", created.SandboxID, err)
	}
	if info.ID != created.SandboxID {
		t.Errorf("InspectSandbox id = %q, want %q", info.ID, created.SandboxID)
	}
	if info.State != runtime.StateRunning {
		t.Errorf("InspectSandbox state = %q, want %q", info.State, runtime.StateRunning)
	}

	if state := listedState(t, c, ctx, created.SandboxID); state != store.Running {
		t.Errorf("ListSandboxes reports %q, want %q", state, store.Running)
	}

	if err := c.DestroySandbox(ctx, created.SandboxID); err != nil {
		t.Fatalf("DestroySandbox(%s) error = %v, want nil", created.SandboxID, err)
	}
	// Cleanup is retry safe by contract, and a failure here is what a recovery
	// loop would trip over.
	if err := c.DestroySandbox(ctx, created.SandboxID); err != nil {
		t.Errorf("second DestroySandbox(%s) error = %v, want nil: destroy must be idempotent", created.SandboxID, err)
	}

	// A conflict rather than a not-found: the row still exists and says stopped,
	// and inspect is only defined for a running sandbox.
	_, err = c.InspectSandbox(ctx, created.SandboxID)
	if !client.HasCode(err, httpapi.CodeConflict) {
		t.Errorf("InspectSandbox after destroy error = %v, want code %q", err, httpapi.CodeConflict)
	}

	// The surviving row is the deliverable, not a leak: the store records what
	// should exist, and a destroyed sandbox is a fact worth keeping. A reconciler
	// (plan 06) reads exactly this.
	if state := listedState(t, c, ctx, created.SandboxID); state != store.Stopped {
		t.Errorf("ListSandboxes reports %q after destroy, want %q", state, store.Stopped)
	}
}

// listedState reports membership as well as state: "absent" distinguishes a row
// the store dropped from one whose state is merely unexpected.
func listedState(t *testing.T, c *client.Client, ctx context.Context, sandboxID string) store.TaskState {
	t.Helper()

	sbxs, err := c.ListSandboxes(ctx)
	if err != nil {
		t.Fatalf("ListSandboxes error = %v, want nil", err)
	}
	for _, sbx := range sbxs {
		if sbx.SandboxID == sandboxID {
			return sbx.State
		}
	}
	return "absent"
}

type liveLogWriter struct{ t *testing.T }

func (w liveLogWriter) Write(p []byte) (int, error) {
	w.t.Log(string(p))
	return len(p), nil
}
