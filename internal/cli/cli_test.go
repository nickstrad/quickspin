package cli_test

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/quickspin/internal/cli"
	"github.com/nickstrad/quickspin/internal/sandbox"
)

const (
	testID  = "sbx_9f8e7d6c-5b4a-4938-8271-60514f3e2d1c"
	otherID = "sbx_2c1d0e9f-8a7b-4c6d-9e5f-4a3b2c1d0e9f"
)

var testTime = time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC)

func execute(t *testing.T, api fakeAPI, args ...string) (string, string, error) {
	t.Helper()

	return executeWithLogs(t, api, io.Discard, args...)
}

func executeWithLogs(
	t *testing.T,
	api fakeAPI,
	logs io.Writer,
	args ...string,
) (string, string, error) {
	t.Helper()

	level := new(slog.LevelVar)
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: level})).
		With("component", "cli")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd := cli.NewCommand(api, logger, level)
	cmd.SetArgs(args)
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	err := cmd.ExecuteContext(t.Context())
	return stdout.String(), stderr.String(), err
}

func TestLogLevelFlagControlsDebugLogging(t *testing.T) {
	api := fakeAPI{
		ListFn: func(context.Context) ([]*sandbox.Sandbox, error) {
			return nil, nil
		},
	}

	tests := []struct {
		name     string
		args     []string
		wantLogs bool
	}{
		{name: "default info level", args: []string{"sandbox", "list"}},
		{name: "debug level", args: []string{"sandbox", "list", "--log-level", "debug"}, wantLogs: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			if _, _, err := executeWithLogs(t, api, &logs, tt.args...); err != nil {
				t.Fatalf("execute list error = %v, want nil", err)
			}

			gotLogs := logs.String()
			if tt.wantLogs {
				if !strings.Contains(gotLogs, `level=DEBUG msg="executing list command" component=cli`) {
					t.Errorf("debug logs = %q, want list command and CLI identity", gotLogs)
				}
			} else if gotLogs != "" {
				t.Errorf("default logs = %q, want debug message suppressed", gotLogs)
			}
		})
	}
}

func TestInvalidLogLevelIsRejected(t *testing.T) {
	_, _, err := execute(t, fakeAPI{}, "--log-level", "trace")
	if err == nil || !strings.Contains(err.Error(), `invalid log level "trace"`) {
		t.Fatalf("execute error = %v, want invalid log level error", err)
	}
}
