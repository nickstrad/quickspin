package sqlite_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/nickstrad/quickspin/internal/sandbox"
	"github.com/nickstrad/quickspin/internal/store"
	"github.com/nickstrad/quickspin/internal/store/sqlite"
	"github.com/nickstrad/quickspin/internal/store/storetest"
)

func newTestStore(t *testing.T, path string) *sqlite.Store {
	t.Helper()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := sqlite.New(context.Background(), path, "", logger)
	if err != nil {
		t.Fatalf("New(%s) error = %v, want nil", path, err)
	}
	t.Cleanup(func() {
		if err := st.Cleanup(); err != nil {
			t.Errorf("Cleanup() error = %v, want nil", err)
		}
	})
	return st
}

func TestSqliteStore(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store {
		return newTestStore(t, ":memory:")
	})
}

func TestNewHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := sqlite.New(ctx, ":memory:", "", logger); !errors.Is(err, context.Canceled) {
		t.Errorf("New() error = %v, want context.Canceled", err)
	}
}

func TestSqliteStoreOperationsHonorCanceledContext(t *testing.T) {
	st := newTestStore(t, ":memory:")

	seedImage := "alpine:3.20"
	sbx, err := st.CreateSandbox(context.Background(), "seed", sandbox.SpecFile{Image: &seedImage}, storetest.TestExpiry())
	if err != nil {
		t.Fatalf("CreateSandbox(seed) error = %v, want nil", err)
	}
	sandboxID := sbx.SandboxID

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	canceledImage := "debian:12"
	tests := []struct {
		name string
		call func() error
	}{
		{
			name: "get idempotency key",
			call: func() error {
				_, err := st.GetIdempotencyKey(ctx, "seed")
				return err
			},
		},
		{
			name: "create idempotency key",
			call: func() error {
				_, err := st.CreateIdempotencyKey(ctx, "canceled-key", sandboxID)
				return err
			},
		},
		{
			name: "get sandbox",
			call: func() error {
				_, err := st.GetSandbox(ctx, sandboxID)
				return err
			},
		},
		{
			name: "get sandboxes",
			call: func() error {
				_, err := st.GetSandboxes(ctx)
				return err
			},
		},
		{
			name: "update sandbox state",
			call: func() error {
				_, err := st.UpdateSandboxState(ctx, sandboxID, sandbox.Pending, sandbox.Running, "canceled transition")
				return err
			},
		},
		{
			name: "update sandbox expiry",
			call: func() error {
				_, err := st.UpdateSandboxExpiry(ctx, sandboxID, storetest.TestExpiry().Add(time.Hour))
				return err
			},
		},
		{
			name: "create sandbox",
			call: func() error {
				_, err := st.CreateSandbox(ctx, "canceled-create", sandbox.SpecFile{Image: &canceledImage}, storetest.TestExpiry())
				return err
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.call(); !errors.Is(err, context.Canceled) {
				t.Errorf("operation error = %v, want context.Canceled", err)
			}
		})
	}

	got, err := st.GetSandbox(context.Background(), sandboxID)
	if err != nil {
		t.Fatalf("GetSandbox(%s) after canceled operations error = %v, want nil", sandboxID, err)
	}
	if got.State != sandbox.Pending {
		t.Errorf("State after canceled update = %q, want %q", got.State, sandbox.Pending)
	}
	key, err := st.GetIdempotencyKey(context.Background(), "canceled-key")
	if err != nil {
		t.Fatalf("GetIdempotencyKey(canceled-key) error = %v, want nil", err)
	}
	if key != nil {
		t.Errorf("GetIdempotencyKey(canceled-key) = %#v, want nil after canceled insert", key)
	}
}
