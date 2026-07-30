package runtime

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestRemovePathExecsRmWithThePathAsOneArgument(t *testing.T) {
	const path = "/work/remove-me;touch${IFS}pwned"

	daemon := newFakeDaemon(t)
	daemon.list = listOKManaged()
	rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

	if err := rt.RemovePath(t.Context(), testSandboxID, path); err != nil {
		t.Fatalf("RemovePath error = %v, want nil", err)
	}

	request := daemon.lastMatchingPath(http.MethodPost, "/containers/"+testContainerID+"/exec")
	var body struct {
		Cmd []string
	}
	if err := json.Unmarshal(request.body, &body); err != nil {
		t.Fatalf("decode exec-create body: %v", err)
	}
	if want := []string{"rm", "-rf", path}; !slices.Equal(body.Cmd, want) {
		t.Errorf("exec command = %q, want %q: the path must be one argv element", body.Cmd, want)
	}
}

func TestRemovePathRejectsInvalidPathBeforeDocker(t *testing.T) {
	tests := []struct {
		name string
		path string
	}{
		{name: "empty", path: ""},
		{name: "relative", path: "work/main.go"},
		{name: "traversal", path: "/work/../etc/passwd"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			daemon := newFakeDaemon(t)
			rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

			err := rt.RemovePath(t.Context(), testSandboxID, tt.path)
			if !errors.Is(err, ErrInvalidPath) {
				t.Fatalf("RemovePath(%q) error = %v, want ErrInvalidPath", tt.path, err)
			}
			if got := daemon.routes(); len(got) != 0 {
				t.Errorf("requests = %v, want none for a path rejected before I/O", got)
			}
		})
	}
}

func TestRemovePathReportsAFailedRm(t *testing.T) {
	daemon := newFakeDaemon(t)
	daemon.list = listOKManaged()
	daemon.execStart = hijackExec(t, nil, []byte("rm: permission denied\n"))
	daemon.execInspect = func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"ID": "exec-good", "Running": false, "ExitCode": 1})
	}
	rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

	err := rt.RemovePath(t.Context(), testSandboxID, "/work/protected")
	if err == nil || !strings.Contains(err.Error(), "permission denied") {
		t.Fatalf("RemovePath error = %v, want rm's stderr", err)
	}
}

func TestRemovePathKeepsTheIdentitySentinelsDistinct(t *testing.T) {
	tests := []struct {
		name string
		id   string
		want error
	}{
		{name: "malformed sandbox id", id: "not-a-sandbox-id", want: ErrInvalidSandboxID},
		{name: "unknown sandbox id", id: testSandboxID, want: ErrNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			daemon := newFakeDaemon(t)
			rt, _ := newDockerTestRuntime(t, slog.LevelInfo, daemon)

			err := rt.RemovePath(t.Context(), tt.id, "/work/main.go")
			if !errors.Is(err, tt.want) {
				t.Fatalf("RemovePath error = %v, want %v", err, tt.want)
			}
		})
	}
}
