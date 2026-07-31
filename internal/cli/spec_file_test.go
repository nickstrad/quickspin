package cli_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/nickstrad/quickspin/internal/store"
)

// writeSpec puts content in a temp file and returns its path, so a test can
// exercise the flag the way a caller would rather than the decoder directly.
func writeSpec(t *testing.T, name, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write spec file: %v", err)
	}
	return path
}

func createdSpec(t *testing.T, args ...string) (runtime.Spec, error) {
	t.Helper()

	var sent store.SpecFile
	_, _, err := execute(t, recordingCreate(&sent), args...)
	if err != nil {
		return runtime.Spec{}, err
	}
	return mustResolve(t, sent), nil
}

func rejectedBefore(t *testing.T, api fakeAPI, wantMsg string, args ...string) {
	t.Helper()

	_, _, err := execute(t, api, args...)
	if err == nil || !strings.Contains(err.Error(), wantMsg) {
		t.Fatalf("execute error = %v, want it to name %q", err, wantMsg)
	}
}

// The same spec in both notations must land identically: the decoder is handed
// YAML either way, since YAML is a superset of JSON.
func TestCreateReadsSpecFromYAMLOrJSON(t *testing.T) {
	files := map[string]string{
		"spec.yaml": "image: alpine:3.20\ncpus: 0.5\nmemory: 64m\npids-limit: 64\nallow-network: true\nenv:\n  GREETING: hello\n",
		"spec.json": `{"image": "alpine:3.20", "cpus": 0.5, "memory": "64m", "pids-limit": 64, "allow-network": true, "env": {"GREETING": "hello"}}`,
	}

	for name, content := range files {
		t.Run(name, func(t *testing.T) {
			spec, err := createdSpec(t, "sandbox", "create", "--file", writeSpec(t, name, content))
			if err != nil {
				t.Fatalf("execute create error = %v, want nil", err)
			}

			want := runtime.NewSpec(
				"alpine:3.20",
				map[string]string{"GREETING": "hello"},
				0.5,
				64*1024*1024,
				64,
				true,
			)
			if spec.Image != want.Image || spec.CPULimit != want.CPULimit ||
				spec.MemoryLimit != want.MemoryLimit || spec.PidsLimit != want.PidsLimit ||
				spec.AllowNetwork != want.AllowNetwork || spec.Env["GREETING"] != "hello" {
				t.Errorf("spec = %#v, want %#v", spec, want)
			}
		})
	}
}

func TestCreateFlagsOverrideTheSpecFile(t *testing.T) {
	path := writeSpec(t, "spec.yaml", "image: alpine:3.20\nmemory: 64m\nenv:\n  GREETING: hello\n  NAME: world\n")

	spec, err := createdSpec(t, "sandbox", "create", "debian:12",
		"--file", path,
		"--memory", "128m",
		"-e", "NAME=override",
	)
	if err != nil {
		t.Fatalf("execute create error = %v, want nil", err)
	}

	if spec.Image != "debian:12" {
		t.Errorf("Image = %q, want the argument to beat the file's image", spec.Image)
	}
	if want := int64(128 * 1024 * 1024); spec.MemoryLimit != want {
		t.Errorf("MemoryLimit = %d, want %d from --memory", spec.MemoryLimit, want)
	}
	// Per-key merge, not whole-field replacement: one -e must not discard the
	// file's other variables.
	if spec.Env["NAME"] != "override" || spec.Env["GREETING"] != "hello" {
		t.Errorf("Env = %#v, want NAME overridden and GREETING kept", spec.Env)
	}
}

// A key the file omits still gets its default, so a partial file is enough.
func TestCreateSpecFileOmissionsFallBackToDefaults(t *testing.T) {
	spec, err := createdSpec(t, "sandbox", "create",
		"--file", writeSpec(t, "spec.yaml", "image: alpine:3.20\n"),
	)
	if err != nil {
		t.Fatalf("execute create error = %v, want nil", err)
	}

	if err := spec.Validate(); err != nil {
		t.Errorf("spec from a minimal file does not validate: %v", err)
	}
	if spec.AllowNetwork {
		t.Error("AllowNetwork = true, want default-deny when the file is silent")
	}
}

func TestCreateSpecFileIsRejectedWhenUnusable(t *testing.T) {
	tests := []struct {
		name    string
		content string
		wantMsg string
	}{
		{
			// Not silently ignored: a typo that parsed cleanly would produce a
			// sandbox with the default limit and no sign the file was disregarded.
			name:    "unknown key",
			content: "image: alpine:3.20\nmemoy: 64m\n",
			wantMsg: "field memoy not found",
		},
		{
			name:    "empty file",
			content: "",
			wantMsg: "file is empty",
		},
		{
			// yaml.Decoder reads one document per call, so the second would
			// otherwise be dropped without a word.
			name:    "two documents",
			content: "image: alpine:3.20\n---\nimage: debian:12\n",
			wantMsg: "more than one document",
		},
		{
			name:    "no image in file or argument",
			content: "cpus: 0.5\n",
			wantMsg: "no image",
		},
		{
			// An explicit zero is not "unset": it has to reach Validate and be
			// rejected, exactly as --cpus 0 is.
			name:    "explicit zero limit",
			content: "image: alpine:3.20\ncpus: 0\n",
			wantMsg: "cpu limit",
		},
		{
			name:    "malformed yaml",
			content: "image: [alpine:3.20\n",
			wantMsg: "read spec file",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rejectedBefore(t, fakeAPI{}, tt.wantMsg,
				"sandbox", "create", "--file", writeSpec(t, "spec.yaml", tt.content),
			)
		})
	}
}

func TestCreateMissingSpecFileIsReported(t *testing.T) {
	_, err := createdSpec(t, "sandbox", "create", "--file", "/nonexistent/spec.yaml")
	if err == nil || !strings.Contains(err.Error(), "open spec file") {
		t.Fatalf("execute create error = %v, want an open failure", err)
	}
}

func TestExecReadsTheRequestFromASpecFile(t *testing.T) {
	var got execCall
	api := recordingExec(&got, runtime.ExecResult{})

	path := writeSpec(t, "task.yaml",
		"command: [sh, -c, echo hello]\nworkdir: /tmp\ntimeout: 5s\nenv:\n  MODE: debug\n",
	)
	if _, _, err := execute(t, api, "sandbox", "exec", testID, "--file", path); err != nil {
		t.Fatalf("execute exec error = %v, want nil", err)
	}

	if want := []string{"sh", "-c", "echo hello"}; !slices.Equal(got.cmd, want) {
		t.Errorf("Exec cmd = %v, want %v", got.cmd, want)
	}
	if got.opts.WorkDir != "/tmp" {
		t.Errorf("WorkDir = %q, want /tmp", got.opts.WorkDir)
	}
	// The file carries a duration string, since a bare number would leave the
	// unit unstated.
	if got.opts.Timeout != 5*time.Second {
		t.Errorf("Timeout = %v, want 5s", got.opts.Timeout)
	}
	if got.opts.Env["MODE"] != "debug" {
		t.Errorf("Env = %#v, want MODE=debug", got.opts.Env)
	}
}

func TestExecFlagsAndArgumentsOverrideTheSpecFile(t *testing.T) {
	var got execCall
	api := recordingExec(&got, runtime.ExecResult{})

	path := writeSpec(t, "task.yaml", "command: [true]\nworkdir: /tmp\ntimeout: 5s\n")
	if _, _, err := execute(t, api,
		"sandbox", "exec", testID, "--file", path, "-w", "/app", "--", "echo", "hi",
	); err != nil {
		t.Fatalf("execute exec error = %v, want nil", err)
	}

	if want := []string{"echo", "hi"}; !slices.Equal(got.cmd, want) {
		t.Errorf("Exec cmd = %v, want the trailing command to beat the file", got.cmd)
	}
	if got.opts.WorkDir != "/app" {
		t.Errorf("WorkDir = %q, want /app from -w", got.opts.WorkDir)
	}
	if got.opts.Timeout != 5*time.Second {
		t.Errorf("Timeout = %v, want the file's 5s to survive an unset flag", got.opts.Timeout)
	}
}

func TestExecIsRejectedWhenTheRequestIsUnusable(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantMsg string
	}{
		{
			name:    "no command from either source",
			args:    []string{"sandbox", "exec", testID},
			wantMsg: "no command",
		},
		{
			// A bare number leaves the unit unstated, so it is refused rather than
			// guessed at.
			name: "timeout without a unit",
			args: []string{"sandbox", "exec", testID, "--file",
				writeSpec(t, "task.yaml", "command: [true]\ntimeout: 5\n")},
			wantMsg: "invalid timeout",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rejectedBefore(t, fakeAPI{}, tt.wantMsg, tt.args...)
		})
	}
}
