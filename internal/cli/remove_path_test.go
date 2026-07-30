package cli_test

import (
	"context"
	"errors"
	"testing"

	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/nickstrad/quickspin/internal/runtime/runtimetest"
)

func TestRemovePathPassesTheTargetAndStaysSilent(t *testing.T) {
	var gotID, gotPath string
	rt := runtimetest.Fake{
		RemovePathFn: func(_ context.Context, id, path string) error {
			gotID, gotPath = id, path
			return nil
		},
	}

	stdout, _, err := execute(t, rt, "sandbox", "rm", testID, "/work/build")
	if err != nil {
		t.Fatalf("execute rm error = %v, want nil", err)
	}
	if gotID != testID || gotPath != "/work/build" {
		t.Errorf("RemovePath target = %q:%s, want %q:/work/build", gotID, gotPath, testID)
	}
	if stdout != "" {
		t.Errorf("rm output = %q, want silent success", stdout)
	}
}

func TestRemovePathPreservesRuntimeSentinels(t *testing.T) {
	rt := runtimetest.Fake{
		RemovePathFn: func(context.Context, string, string) error {
			return runtime.E("runtime.DockerRuntime.RemovePath", "resolving sandbox", runtime.ErrNotFound)
		},
	}

	_, _, err := execute(t, rt, "sandbox", "rm", testID, "/work/build")
	if !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("execute rm error = %v, want errors.Is(..., ErrNotFound)", err)
	}
}
