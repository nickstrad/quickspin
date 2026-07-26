package runtime

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/client"
)

func TestNewDockerRuntimeRequiresLogger(t *testing.T) {
	_, err := NewDockerRuntime(&client.Client{}, nil)
	if err == nil || !strings.Contains(err.Error(), "logger is required") {
		t.Fatalf("NewDockerRuntime error = %v, want required logger error", err)
	}
}

func TestListLogsSkippedContainerAndResultCount(t *testing.T) {
	rt, logs := newDockerTestRuntime(t, slog.LevelDebug, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/containers/json") {
			http.Error(w, "unexpected request", http.StatusNotFound)
			return
		}

		containers := []container.Summary{
			{
				ID:     "container-bad",
				Labels: managedMarkerLabels(),
				State:  container.StateRunning,
			},
			{
				ID:     "container-good",
				Labels: managedLabels("sbx_9f8e7d6c-5b4a-4938-8271-60514f3e2d1c"),
				State:  container.StateRunning,
			},
		}
		if err := json.NewEncoder(w).Encode(containers); err != nil {
			t.Errorf("encode containers: %v", err)
		}
	})

	infos, err := rt.List(t.Context())
	if err != nil {
		t.Fatalf("List error = %v, want nil", err)
	}
	if len(infos) != 1 {
		t.Fatalf("List infos = %#v, want one valid sandbox", infos)
	}

	gotLogs := logs.String()
	if !strings.Contains(gotLogs, `level=WARN msg="skipping managed container with invalid sandbox label" containerID=container-bad`) {
		t.Errorf("List logs = %q, want warning with skipped container identity", gotLogs)
	}
	if !strings.Contains(gotLogs, `level=DEBUG msg="listed managed sandboxes" count=1`) {
		t.Errorf("List logs = %q, want debug result count", gotLogs)
	}
}

func TestDestroyLogsLifecycleAtInfo(t *testing.T) {
	const (
		sandboxID   = "sbx_9f8e7d6c-5b4a-4938-8271-60514f3e2d1c"
		containerID = "container-good"
	)

	rt, logs := newDockerTestRuntime(t, slog.LevelInfo, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/containers/json"):
			if err := json.NewEncoder(w).Encode([]container.Summary{{
				ID:     containerID,
				Labels: managedLabels(sandboxID),
			}}); err != nil {
				t.Errorf("encode containers: %v", err)
			}
		case r.Method == http.MethodDelete && strings.HasSuffix(r.URL.Path, "/containers/"+containerID):
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected request", http.StatusNotFound)
		}
	})

	if err := rt.Destroy(t.Context(), sandboxID); err != nil {
		t.Fatalf("Destroy error = %v, want nil", err)
	}

	if got := logs.String(); !strings.Contains(got, `level=INFO msg="sandbox destroyed" sandboxID=`+sandboxID+` containerID=`+containerID) {
		t.Errorf("Destroy logs = %q, want info lifecycle event with both identities", got)
	}
}

func newDockerTestRuntime(
	t *testing.T,
	level slog.Level,
	handler http.HandlerFunc,
) (*DockerRuntime, *bytes.Buffer) {
	t.Helper()

	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	dockerClient, err := client.New(
		client.WithHost(server.URL),
		client.WithAPIVersion(client.MaxAPIVersion),
	)
	if err != nil {
		t.Fatalf("new Docker test client: %v", err)
	}
	t.Cleanup(func() { _ = dockerClient.Close() })

	logs := new(bytes.Buffer)
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: level}))
	rt, err := NewDockerRuntime(dockerClient, logger)
	if err != nil {
		t.Fatalf("NewDockerRuntime error = %v, want nil", err)
	}

	return rt, logs
}
