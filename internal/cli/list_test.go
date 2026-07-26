package cli_test

import (
	"context"
	"testing"
	"time"

	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/nickstrad/quickspin/internal/runtime/runtimetest"
)

func TestListWritesStableJSON(t *testing.T) {
	infos := []runtime.Info{
		{ID: testID, State: runtime.StateStopped, CreatedAt: testTime.Add(time.Minute)},
		{ID: otherID, State: runtime.StateRunning, CreatedAt: testTime},
	}
	rt := runtimetest.Fake{
		ListFn: func(context.Context) ([]runtime.Info, error) {
			return infos, nil
		},
	}

	stdout, _, err := execute(t, rt, "--output", "json", "sandbox", "list")
	if err != nil {
		t.Fatalf("execute list error = %v, want nil", err)
	}

	want := `[
  {
    "id": "sbx_2c1d0e9f-8a7b-4c6d-9e5f-4a3b2c1d0e9f",
    "state": "running",
    "created_at": "2026-07-25T12:00:00Z"
  },
  {
    "id": "sbx_9f8e7d6c-5b4a-4938-8271-60514f3e2d1c",
    "state": "stopped",
    "created_at": "2026-07-25T12:01:00Z"
  }
]
`
	if stdout != want {
		t.Errorf("list output =\n%s\nwant\n%s", stdout, want)
	}
	if infos[0].ID != testID {
		t.Error("list sorted the runtime-owned slice in place, want a copied slice")
	}
}

func TestEmptyListWritesAnEmptyJSONArray(t *testing.T) {
	rt := runtimetest.Fake{
		ListFn: func(context.Context) ([]runtime.Info, error) {
			return nil, nil
		},
	}

	stdout, _, err := execute(t, rt, "sandbox", "list", "-o", "json")
	if err != nil {
		t.Fatalf("execute list error = %v, want nil", err)
	}
	if stdout != "[]\n" {
		t.Errorf("empty list output = %q, want %q", stdout, "[]\n")
	}
}
