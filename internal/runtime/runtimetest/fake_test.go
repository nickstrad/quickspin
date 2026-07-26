package runtimetest_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/nickstrad/quickspin/internal/runtime"
	"github.com/nickstrad/quickspin/internal/runtime/runtimetest"
)

// describe and teardown stand in for the CLI (and later the control plane):
// any code that holds a runtime.Runtime and turns its answers into output or
// an error. They live in the test because the real consumers do not exist yet;
// what is being demonstrated is that the Fake can drive one.

func describe(ctx context.Context, rt runtime.Runtime, out io.Writer, id string) error {
	info, err := rt.Inspect(ctx, id)
	if err != nil {
		return err
	}

	fmt.Fprintf(out, "%s\t%s\t%s\n", info.ID, info.State, info.CreatedAt.Format(time.RFC3339))
	return nil
}

func teardown(ctx context.Context, rt runtime.Runtime, ids ...string) error {
	for _, id := range ids {
		if err := rt.Destroy(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func TestFakeDrivesAConsumerWithoutADaemon(t *testing.T) {
	const id = "sbx_9f8e7d6c-5b4a-4938-8271-60514f3e2d1c"

	var gotID string
	rt := runtimetest.Fake{
		InspectFn: func(_ context.Context, id string) (runtime.Info, error) {
			gotID = id
			return runtime.Info{
				ID:        id,
				State:     runtime.StateRunning,
				CreatedAt: time.Date(2026, 7, 25, 12, 0, 0, 0, time.UTC),
			}, nil
		},
	}

	var out bytes.Buffer
	if err := describe(t.Context(), rt, &out, id); err != nil {
		t.Fatalf("describe() error = %v, want nil", err)
	}

	if gotID != id {
		t.Errorf("Inspect received id %q, want %q", gotID, id)
	}

	want := id + "\trunning\t2026-07-25T12:00:00Z\n"
	if got := out.String(); got != want {
		t.Errorf("describe() wrote %q, want %q", got, want)
	}
}

func TestFakePropagatesSentinelsThroughAConsumer(t *testing.T) {
	// The failure this catches is a consumer that reformats the error with
	// fmt.Errorf("%v"), which breaks errors.Is for everything above it — and
	// which no daemon-backed test would notice, because the daemon's error is
	// correct at the point it is produced.
	rt := runtimetest.Fake{
		InspectFn: func(context.Context, string) (runtime.Info, error) {
			return runtime.Info{}, runtime.E(
				"runtime.dockerRuntime.Inspect",
				"listing containers by label",
				runtime.ErrNotFound,
			)
		},
	}

	err := describe(t.Context(), rt, io.Discard, "sbx_9f8e7d6c-5b4a-4938-8271-60514f3e2d1c")
	if !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("describe() error = %v, want errors.Is(..., ErrNotFound)", err)
	}
	if errors.Is(err, runtime.ErrImageMissing) {
		t.Error("describe() error matched ErrImageMissing, want only ErrNotFound in the chain")
	}
}

func TestFakeMakesIdempotentDestroyStatable(t *testing.T) {
	// The interface promises Destroy of an unknown id returns nil. Stating it
	// here means a consumer that special-cases "already gone" — the exact
	// duplication the promise exists to prevent — fails without a container.
	calls := 0
	rt := runtimetest.Fake{
		DestroyFn: func(context.Context, string) error {
			calls++
			return nil
		},
	}

	const id = "sbx_9f8e7d6c-5b4a-4938-8271-60514f3e2d1c"
	if err := teardown(t.Context(), rt, id, id); err != nil {
		t.Fatalf("teardown() twice = %v, want nil both times", err)
	}
	if calls != 2 {
		t.Errorf("Destroy called %d times, want 2: the consumer must not skip a repeat destroy", calls)
	}
}

func TestFakePanicsOnAnUnsetMethod(t *testing.T) {
	// The nil embedded interface is what makes an unexpected call loud. A Fake
	// that returned zero values here would let a consumer silently create a
	// sandbox during a test that only meant to inspect one.
	defer func() {
		if recover() == nil {
			t.Error("Create on a Fake with no CreateFn returned normally, want a panic")
		}
	}()

	rt := runtimetest.Fake{InspectFn: func(context.Context, string) (runtime.Info, error) {
		return runtime.Info{}, nil
	}}
	_, _ = rt.Create(t.Context(), runtime.Spec{Image: "alpine:3.20"})
}
