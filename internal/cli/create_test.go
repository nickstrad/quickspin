package cli_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/nickstrad/quickspin/internal/sandbox"
)

func recordingCreate(got *sandbox.SpecFile) fakeAPI {
	return fakeAPI{
		CreateFn: func(_ context.Context, _ string, spec sandbox.SpecFile, _ time.Duration) (*sandbox.Sandbox, error) {
			*got = spec
			return sandboxRecord(testID, "alpine:3.20", sandbox.Running), nil
		},
	}
}

func mustResolve(t *testing.T, spec sandbox.SpecFile) runtime.Spec {
	t.Helper()

	resolved, err := spec.Resolve()
	if err != nil {
		t.Fatalf("Resolve() error = %v, want nil", err)
	}
	return resolved
}

func TestCreatePassesImageAndEnvironmentAndWritesATable(t *testing.T) {
	var sent sandbox.SpecFile
	api := recordingCreate(&sent)

	stdout, _, err := execute(t, api,
		"sandbox", "create", "alpine:3.20",
		"--env", "GREETING=hello world",
		"--env", "EMPTY=",
	)
	if err != nil {
		t.Fatalf("execute create error = %v, want nil", err)
	}

	gotSpec := mustResolve(t, sent)
	if gotSpec.Image != "alpine:3.20" {
		t.Errorf("Create spec image = %q, want %q", gotSpec.Image, "alpine:3.20")
	}
	if gotSpec.Env["GREETING"] != "hello world" || gotSpec.Env["EMPTY"] != "" {
		t.Errorf("Create spec env = %#v, want GREETING and EMPTY values preserved", gotSpec.Env)
	}

	want := "" +
		"ID                                        STATE    IMAGE        CREATED AT            EXPIRES AT\n" +
		testID + "  running  alpine:3.20  2026-07-25T12:00:00Z  2026-07-25T12:15:00Z\n"
	if stdout != want {
		t.Errorf("create output =\n%q\nwant\n%q", stdout, want)
	}
}

func TestCreateLimitFlagsReachTheSpec(t *testing.T) {
	var sent sandbox.SpecFile
	api := recordingCreate(&sent)

	_, _, err := execute(t, api,
		"sandbox", "create", "alpine:3.20",
		"--cpus", "0.5",
		"--memory", "64m",
		"--pids-limit", "64",
		"--allow-network",
	)
	if err != nil {
		t.Fatalf("execute create error = %v, want nil", err)
	}

	gotSpec := mustResolve(t, sent)
	if gotSpec.CPULimit != 0.5 {
		t.Errorf("CPULimit = %v, want 0.5 cores", gotSpec.CPULimit)
	}
	// --memory is a size, not a byte count: "64m" has to arrive as bytes, since
	// Spec.MemoryLimit is what becomes memory.max.
	if want := int64(64 * 1024 * 1024); gotSpec.MemoryLimit != want {
		t.Errorf("MemoryLimit = %d, want %d bytes for 64m", gotSpec.MemoryLimit, want)
	}
	if gotSpec.PidsLimit != 64 {
		t.Errorf("PidsLimit = %d, want 64", gotSpec.PidsLimit)
	}
	if !gotSpec.AllowNetwork {
		t.Error("AllowNetwork = false, want true when --allow-network is given")
	}
}

// An omitted --ttl sends zero rather than a client-side default: the server
// owns the default, so the two can never drift apart.
func TestCreateSendsTheRequestedTTL(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want time.Duration
	}{
		{name: "explicit", args: []string{"--ttl", "30m"}, want: 30 * time.Minute},
		{name: "omitted", want: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var sent time.Duration
			api := fakeAPI{
				CreateFn: func(_ context.Context, _ string, _ sandbox.SpecFile, ttl time.Duration) (*sandbox.Sandbox, error) {
					sent = ttl
					return sandboxRecord(testID, "alpine:3.20", sandbox.Running), nil
				},
			}

			if _, _, err := execute(t, api, append([]string{"sandbox", "create"}, tt.args...)...); err != nil {
				t.Fatalf("execute create error = %v, want nil", err)
			}
			if sent != tt.want {
				t.Errorf("ttl sent = %v, want %v", sent, tt.want)
			}
		})
	}
}

func TestCreateWithoutFlagsUsesDefaults(t *testing.T) {
	var sent sandbox.SpecFile
	api := recordingCreate(&sent)

	if _, _, err := execute(t, api, "sandbox", "create"); err != nil {
		t.Fatalf("execute create error = %v, want nil", err)
	}

	gotSpec := mustResolve(t, sent)
	if gotSpec.Image != sandbox.DefaultImage {
		t.Errorf("Image = %q, want %q", gotSpec.Image, sandbox.DefaultImage)
	}
	if err := gotSpec.Validate(); err != nil {
		t.Errorf("default spec does not validate: %v", err)
	}
	if gotSpec.AllowNetwork {
		t.Error("AllowNetwork = true by default, want default-deny")
	}
}

func TestCreateRejectsUnenforceableLimitsBeforeCallingCreate(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantMsg string
	}{
		{
			// Not "unlimited": Spec has no spelling for that, and a zero would
			// have reached Docker as exactly that.
			name:    "zero memory",
			args:    []string{"--memory", "0"},
			wantMsg: "memory limit",
		},
		{
			name:    "memory below the floor",
			args:    []string{"--memory", "1m"},
			wantMsg: "memory limit",
		},
		{
			name:    "zero pids",
			args:    []string{"--pids-limit", "0"},
			wantMsg: "pids limit",
		},
		{
			name:    "zero cpus",
			args:    []string{"--cpus", "0"},
			wantMsg: "cpu limit",
		},
		{
			// Rejected at parse time rather than silently truncated: a "1.5g" that
			// became 1g would set a limit the caller did not ask for.
			name:    "fractional memory size",
			args:    []string{"--memory", "1.5g"},
			wantMsg: "invalid memory limit",
		},
		{
			name:    "unknown memory suffix",
			args:    []string{"--memory", "512tb"},
			wantMsg: "invalid memory limit",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{"sandbox", "create", "alpine:3.20"}, tt.args...)
			_, _, err := execute(t, fakeAPI{}, args...)
			if err == nil || !strings.Contains(err.Error(), tt.wantMsg) {
				t.Fatalf("execute create error = %v, want it to name %q", err, tt.wantMsg)
			}
		})
	}
}

func TestInvalidEnvironmentStopsBeforeCreate(t *testing.T) {
	_, _, err := execute(t, fakeAPI{}, "sandbox", "create", "alpine:3.20", "--env", "MISSING_VALUE")
	if err == nil || !strings.Contains(err.Error(), "expected KEY=VALUE") {
		t.Fatalf("execute create error = %v, want KEY=VALUE validation error", err)
	}
}

func TestCreateSendsAFreshIdempotencyKey(t *testing.T) {
	var keys []string
	api := fakeAPI{
		CreateFn: func(_ context.Context, key string, _ sandbox.SpecFile, _ time.Duration) (*sandbox.Sandbox, error) {
			keys = append(keys, key)
			return sandboxRecord(testID, "alpine:3.20", sandbox.Running), nil
		},
	}

	for range 2 {
		if _, _, err := execute(t, api, "sandbox", "create", "alpine:3.20"); err != nil {
			t.Fatalf("execute create error = %v, want nil", err)
		}
	}

	if len(keys) != 2 || keys[0] == "" || keys[0] == keys[1] {
		t.Errorf("idempotency keys = %v, want two distinct non-empty keys", keys)
	}
}
