package store_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"testing"

	"github.com/nickstrad/quickspin/internal/store"
	"github.com/nickstrad/quickspin/internal/store/storetest"
)

func TestSqlliteStore(t *testing.T) {
	storetest.Run(t, func(t *testing.T) store.Store {
		t.Helper()

		logger := slog.New(slog.NewTextHandler(io.Discard, nil))
		st, err := store.NewSqlliteStore(context.Background(), ":memory:", "", logger)
		if err != nil {
			t.Fatalf("NewSqlliteStore(:memory:) error = %v, want nil", err)
		}
		t.Cleanup(func() {
			if err := st.Cleanup(); err != nil {
				t.Errorf("Cleanup() error = %v, want nil", err)
			}
		})
		return st
	})
}

func TestNewSqlliteStoreHonorsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := store.NewSqlliteStore(ctx, ":memory:", "", logger); !errors.Is(err, context.Canceled) {
		t.Errorf("NewSqlliteStore() error = %v, want context.Canceled", err)
	}
}

func TestSqlliteStoreOperationsHonorCanceledContext(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.NewSqlliteStore(context.Background(), ":memory:", "", logger)
	if err != nil {
		t.Fatalf("NewSqlliteStore(:memory:) error = %v, want nil", err)
	}
	t.Cleanup(func() {
		if err := st.Cleanup(); err != nil {
			t.Errorf("Cleanup() error = %v, want nil", err)
		}
	})

	sandbox, err := st.CreateSandbox(context.Background(), "seed", `{"image":"alpine:3.20"}`)
	if err != nil {
		t.Fatalf("CreateSandbox(seed) error = %v, want nil", err)
	}
	sandboxID := strconv.Itoa(sandbox.ID)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

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
				_, err := st.CreateIdempotencyKey(ctx, "canceled-key", sandbox.ID)
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
			name: "update sandbox state",
			call: func() error {
				_, err := st.UpdateSandboxState(ctx, string(store.Pending), string(store.Running), sandboxID)
				return err
			},
		},
		{
			name: "create sandbox",
			call: func() error {
				_, err := st.CreateSandbox(ctx, "canceled-create", `{"image":"debian:12"}`)
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
	if got.State != store.Pending {
		t.Errorf("State after canceled update = %q, want %q", got.State, store.Pending)
	}
	key, err := st.GetIdempotencyKey(context.Background(), "canceled-key")
	if err != nil {
		t.Fatalf("GetIdempotencyKey(canceled-key) error = %v, want nil", err)
	}
	if key != nil {
		t.Errorf("GetIdempotencyKey(canceled-key) = %#v, want nil after canceled insert", key)
	}
}
