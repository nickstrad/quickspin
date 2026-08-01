package cli_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nickstrad/quickspin/internal/sandbox"
)

func TestListWritesStableJSON(t *testing.T) {
	newer := sandboxRecord(testID, "alpine:3.20", sandbox.Stopped)
	newer.CreatedAt = testTime.Add(time.Minute)
	newer.UpdatedAt = newer.CreatedAt
	older := sandboxRecord(otherID, "alpine:3.20", sandbox.Running)

	sandboxes := []*sandbox.Sandbox{newer, older}
	api := fakeAPI{
		ListFn: func(context.Context) ([]*sandbox.Sandbox, error) {
			return sandboxes, nil
		},
	}

	stdout, _, err := execute(t, api, "--output", "json", "sandbox", "list")
	if err != nil {
		t.Fatalf("execute list error = %v, want nil", err)
	}

	var got []*sandbox.Sandbox
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("decode list output %q: %v", stdout, err)
	}
	if len(got) != 2 || got[0].SandboxID != otherID || got[1].SandboxID != testID {
		t.Fatalf("list output = %+v, want records oldest first", got)
	}
	if got[0].State != sandbox.Running {
		t.Errorf("first record state = %q, want %q", got[0].State, sandbox.Running)
	}
	if sandboxes[0].SandboxID != testID {
		t.Error("list sorted the client-owned slice in place, want a copied slice")
	}
}

func TestEmptyListWritesAnEmptyJSONArray(t *testing.T) {
	api := fakeAPI{
		ListFn: func(context.Context) ([]*sandbox.Sandbox, error) {
			return nil, nil
		},
	}

	stdout, _, err := execute(t, api, "sandbox", "list", "-o", "json")
	if err != nil {
		t.Fatalf("execute list error = %v, want nil", err)
	}
	if stdout != "[]\n" {
		t.Errorf("empty list output = %q, want %q", stdout, "[]\n")
	}
}
