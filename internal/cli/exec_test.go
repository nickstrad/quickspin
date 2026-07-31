package cli_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/quickspin/internal/runtime"
)

// execCall records what the CLI handed the runtime, so a test can assert the
// translation without reasoning about a container.
type execCall struct {
	id   string
	cmd  []string
	opts runtime.ExecOpts
}

func recordingExec(got *execCall, result runtime.ExecResult) fakeAPI {
	return fakeAPI{
		ExecFn: func(_ context.Context, id string, cmd []string, opts runtime.ExecOpts) (runtime.ExecResult, error) {
			*got = execCall{id: id, cmd: cmd, opts: opts}
			return result, nil
		},
	}
}

func TestExecPassesTheCommandAfterDashDashUntouched(t *testing.T) {
	// Everything after `--` belongs to the sandbox. The flags in this command line
	// are the container's, and quickspin must neither consume nor reorder them —
	// `-e` here is grep's, not quickspin's --env.
	var got execCall
	api := recordingExec(&got, runtime.ExecResult{})

	if _, _, err := execute(t, api,
		"sandbox", "exec", testID, "--", "grep", "-e", "memory.max", "/proc/self/cgroup",
	); err != nil {
		t.Fatalf("execute exec error = %v, want nil", err)
	}

	if got.id != testID {
		t.Errorf("Exec id = %q, want %q", got.id, testID)
	}
	wantCmd := []string{"grep", "-e", "memory.max", "/proc/self/cgroup"}
	if !slices.Equal(got.cmd, wantCmd) {
		t.Errorf("Exec cmd = %v, want %v", got.cmd, wantCmd)
	}
}

func TestExecSeparatesTheCommandsStreams(t *testing.T) {
	// The stdcopy demux upstream exists to split these two; writing both to
	// stdout here would throw that away at the last hop. Neither goes through the
	// renderer: `exec <id> -- cat memory.max` must yield the file, not a table.
	var got execCall
	api := recordingExec(&got, runtime.ExecResult{
		Stdout: []byte("67108864\n"),
		Stderr: []byte("warning: cgroup v1 fallback\n"),
	})

	stdout, stderr, err := execute(t, api, "sandbox", "exec", testID, "--", "cat", "memory.max")
	if err != nil {
		t.Fatalf("execute exec error = %v, want nil", err)
	}

	if stdout != "67108864\n" {
		t.Errorf("stdout = %q, want the command's stdout verbatim", stdout)
	}
	if stderr != "warning: cgroup v1 fallback\n" {
		t.Errorf("stderr = %q, want the command's stderr verbatim", stderr)
	}
}

func TestExecWarnsWhenAStreamWasTruncated(t *testing.T) {
	// The flags exist so a short buffer is not silent; a CLI that drops them
	// leaves the user debugging a parse error in the wrong place. The warning goes
	// to stderr after the command's own output and is prefixed, so a caller
	// reading stdout still gets exactly what the command wrote.
	tests := []struct {
		name       string
		result     runtime.ExecResult
		wantInErr  []string
		wantNoWarn bool
	}{
		{
			name:       "neither",
			result:     runtime.ExecResult{Stdout: []byte("whole\n")},
			wantNoWarn: true,
		},
		{
			name:      "stdout only",
			result:    runtime.ExecResult{Stdout: []byte("cut"), StdoutTruncated: true},
			wantInErr: []string{"quickspin:", "stdout truncated"},
		},
		{
			name: "both",
			result: runtime.ExecResult{
				Stdout: []byte("cut"), Stderr: []byte("also cut"),
				StdoutTruncated: true, StderrTruncated: true,
			},
			wantInErr: []string{"stdout and stderr truncated"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got execCall
			api := recordingExec(&got, tt.result)

			stdout, stderr, err := execute(t, api, "sandbox", "exec", testID, "--", "cat", "big.json")
			if err != nil {
				t.Fatalf("execute exec error = %v, want nil", err)
			}
			if stdout != string(tt.result.Stdout) {
				t.Errorf("stdout = %q, want the command's bytes unchanged", stdout)
			}
			if tt.wantNoWarn && strings.Contains(stderr, "quickspin:") {
				t.Errorf("stderr = %q, want no warning for an untruncated result", stderr)
			}
			for _, want := range tt.wantInErr {
				if !strings.Contains(stderr, want) {
					t.Errorf("stderr = %q, want it to contain %q", stderr, want)
				}
			}
		})
	}
}

func TestExecReportsANonZeroExitCode(t *testing.T) {
	// 137 is the OOM kill plan 03's memory test provokes, so it has to be legible
	// rather than swallowed. It is reported in the error text, not in the process
	// exit status — see the note in exec.go.
	var got execCall
	api := recordingExec(&got, runtime.ExecResult{ExitCode: 137, Stdout: []byte("partial\n")})

	stdout, _, err := execute(t, api, "sandbox", "exec", testID, "--", "sh", "-c", "eat_memory")
	if err == nil || !strings.Contains(err.Error(), "exited 137") {
		t.Fatalf("execute exec error = %v, want the exit code reported", err)
	}
	// Output produced before the kill is still the caller's evidence.
	if stdout != "partial\n" {
		t.Errorf("stdout = %q, want the output written before the failure", stdout)
	}
}

func TestExecTimeoutFlagReachesExecOpts(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want time.Duration
	}{
		{
			// ExecOpts.Timeout has no "forever" value, so an omitted flag must send
			// the documented default rather than a zero the backend would have to
			// reinterpret.
			name: "omitted",
			args: []string{"sandbox", "exec", testID, "--", "sleep", "1"},
			want: 30 * time.Second,
		},
		{
			name: "explicit",
			args: []string{"sandbox", "exec", testID, "--timeout", "1500ms", "--", "sleep", "300"},
			want: 1500 * time.Millisecond,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got execCall
			api := recordingExec(&got, runtime.ExecResult{})

			if _, _, err := execute(t, api, tt.args...); err != nil {
				t.Fatalf("execute exec error = %v, want nil", err)
			}
			if got.opts.Timeout != tt.want {
				t.Errorf("ExecOpts.Timeout = %v, want %v", got.opts.Timeout, tt.want)
			}
		})
	}
}

func TestExecEnvAndWorkdirReachExecOptsNotTheSpec(t *testing.T) {
	// These are per-process, layered over the container's own environment for this
	// call only — unlike create's --env, which is baked into the container.
	var got execCall
	api := recordingExec(&got, runtime.ExecResult{})

	_, _, err := execute(t, api,
		"sandbox", "exec", testID,
		"--env", "LANG=C",
		"--workdir", "/sys/fs/cgroup",
		"--", "cat", "memory.max",
	)
	if err != nil {
		t.Fatalf("execute exec error = %v, want nil", err)
	}

	if got.opts.Env["LANG"] != "C" {
		t.Errorf("ExecOpts.Env = %#v, want LANG=C", got.opts.Env)
	}
	if got.opts.WorkDir != "/sys/fs/cgroup" {
		t.Errorf("ExecOpts.WorkDir = %q, want /sys/fs/cgroup", got.opts.WorkDir)
	}
}

func TestExecRequiresACommand(t *testing.T) {
	if _, _, err := execute(t, fakeAPI{}, "sandbox", "exec", testID); err == nil {
		t.Fatal("execute exec error = nil, want an argument error for a missing command")
	}
}
