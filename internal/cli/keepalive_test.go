package cli_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nickstrad/quickspin/internal/sandbox"
)

func TestKeepaliveSendsTheRequestedTTLAndWritesTheRenewedSandbox(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want time.Duration
	}{
		{name: "explicit ttl", args: []string{"--ttl", "30m"}, want: 30 * time.Minute},
		{name: "omitted ttl lets the server default", want: 0},
		{name: "negative ttl reaches the server", args: []string{"--ttl", "-1s"}, want: -time.Second},
		{name: "ttl above the cap reaches the server", args: []string{"--ttl", "25h"}, want: 25 * time.Hour},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var (
				gotID  string
				gotTTL time.Duration
			)
			renewed := sandboxRecord(testID, "alpine:3.20", sandbox.Running)
			renewed.ExpiresAt = testTime.Add(30 * time.Minute)
			api := fakeAPI{
				KeepaliveFn: func(_ context.Context, id string, ttl time.Duration) (*sandbox.Sandbox, error) {
					gotID, gotTTL = id, ttl
					return renewed, nil
				},
			}

			args := append([]string{"sandbox", "keepalive", testID, "--output", "json"}, tt.args...)
			stdout, _, err := execute(t, api, args...)
			if err != nil {
				t.Fatalf("execute keepalive error = %v, want nil", err)
			}
			if gotID != testID || gotTTL != tt.want {
				t.Errorf("KeepaliveSandbox arguments = (%q, %v), want (%q, %v)", gotID, gotTTL, testID, tt.want)
			}

			var got sandbox.Sandbox
			if err := json.Unmarshal([]byte(stdout), &got); err != nil {
				t.Fatalf("decode keepalive output %q: %v", stdout, err)
			}
			if got.SandboxID != testID || got.State != renewed.State || !got.ExpiresAt.Equal(renewed.ExpiresAt) {
				t.Errorf("keepalive output = %+v, want renewed sandbox %+v", got, renewed)
			}
		})
	}
}

func TestKeepaliveWrapsTheAPIError(t *testing.T) {
	want := errors.New("renewal rejected")
	api := fakeAPI{
		KeepaliveFn: func(context.Context, string, time.Duration) (*sandbox.Sandbox, error) {
			return nil, want
		},
	}

	stdout, _, err := execute(t, api, "sandbox", "keepalive", testID)
	if !errors.Is(err, want) {
		t.Fatalf("execute keepalive error = %v, want wrapped API error", err)
	}
	if got := err.Error(); got != `keep sandbox "`+testID+`" alive: renewal rejected` {
		t.Errorf("execute keepalive error = %q, want command and sandbox context", got)
	}
	if stdout != "" {
		t.Errorf("keepalive output = %q after API error, want empty", stdout)
	}
}
