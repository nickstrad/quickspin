package cli_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/nickstrad/quickspin/internal/runtime/runtimetest"
)

func TestInspectWritesYAML(t *testing.T) {
	var gotID string
	rt := runtimetest.Fake{
		InspectFn: func(_ context.Context, id string) (runtime.Info, error) {
			gotID = id
			return runtime.Info{
				ID:        id,
				State:     runtime.StateRunning,
				CreatedAt: testTime,
			}, nil
		},
	}

	stdout, _, err := execute(t, rt, "sandbox", "inspect", testID, "--output=yaml")
	if err != nil {
		t.Fatalf("execute inspect error = %v, want nil", err)
	}
	if gotID != testID {
		t.Errorf("Inspect id = %q, want %q", gotID, testID)
	}

	want := "" +
		"id: " + testID + "\n" +
		"state: running\n" +
		"created_at: 2026-07-25T12:00:00Z\n"
	if stdout != want {
		t.Errorf("inspect output =\n%s\nwant\n%s", stdout, want)
	}
}

func TestCommandPreservesRuntimeSentinels(t *testing.T) {
	rt := runtimetest.Fake{
		InspectFn: func(context.Context, string) (runtime.Info, error) {
			return runtime.Info{}, runtime.E("runtime.DockerRuntime.Inspect", "resolving sandbox", runtime.ErrNotFound)
		},
	}

	_, _, err := execute(t, rt, "sandbox", "inspect", testID)
	if !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("execute inspect error = %v, want errors.Is(..., ErrNotFound)", err)
	}
}
