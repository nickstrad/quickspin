package cli_test

import (
	"context"
	"strings"
	"testing"

	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/nickstrad/quickspin/internal/runtime/runtimetest"
)

func TestCreatePassesImageAndEnvironmentAndWritesATable(t *testing.T) {
	var gotSpec runtime.Spec
	rt := runtimetest.Fake{
		CreateFn: func(_ context.Context, spec runtime.Spec) (runtime.Info, error) {
			gotSpec = spec
			return runtime.Info{
				ID:        testID,
				State:     runtime.StateRunning,
				CreatedAt: testTime,
			}, nil
		},
	}

	stdout, _, err := execute(t, rt,
		"sandbox", "create", "alpine:3.20",
		"--env", "GREETING=hello world",
		"--env", "EMPTY=",
	)
	if err != nil {
		t.Fatalf("execute create error = %v, want nil", err)
	}

	if gotSpec.Image != "alpine:3.20" {
		t.Errorf("Create spec image = %q, want %q", gotSpec.Image, "alpine:3.20")
	}
	if gotSpec.Env["GREETING"] != "hello world" || gotSpec.Env["EMPTY"] != "" {
		t.Errorf("Create spec env = %#v, want GREETING and EMPTY values preserved", gotSpec.Env)
	}

	want := "" +
		"ID                                        STATE    CREATED AT\n" +
		testID + "  running  2026-07-25T12:00:00Z\n"
	if stdout != want {
		t.Errorf("create output =\n%q\nwant\n%q", stdout, want)
	}
}

func TestInvalidEnvironmentStopsBeforeCreate(t *testing.T) {
	called := false
	rt := runtimetest.Fake{
		CreateFn: func(context.Context, runtime.Spec) (runtime.Info, error) {
			called = true
			return runtime.Info{}, nil
		},
	}

	_, _, err := execute(t, rt, "sandbox", "create", "alpine:3.20", "--env", "MISSING_VALUE")
	if err == nil || !strings.Contains(err.Error(), "expected KEY=VALUE") {
		t.Fatalf("execute create error = %v, want KEY=VALUE validation error", err)
	}
	if called {
		t.Error("Create was called for invalid environment input")
	}
}
