// Package daemon is the server composition root: the one place that picks a
// concrete runtime and store and hands them to the API.
//
// It sits here rather than in control because httpapi imports control, so a
// Serve that reaches back into httpapi would close an import cycle.
package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/nickstrad/quickspin/internal/httpapi"
	"github.com/nickstrad/quickspin/internal/reconciler"
	"github.com/nickstrad/quickspin/internal/runtime/docker"
	"github.com/nickstrad/quickspin/internal/store/sqlite"
)

type Config struct {
	Host               string
	Port               int
	DBPath             string
	Logger             *slog.Logger
	ReconcilerInterval time.Duration
}

// Serve blocks until ctx is done or the server stops.
func Serve(ctx context.Context, cfg Config) error {
	sandboxRuntime, err := docker.New(ctx, nil, cfg.Logger.With(
		"subcomponent", "runtime",
		"backend", "docker",
	))
	if err != nil {
		return fmt.Errorf("open the docker runtime: %w", err)
	}

	sandboxStore, err := sqlite.New(ctx, cfg.DBPath, "", cfg.Logger.With("subcomponent", "store"))
	if err != nil {
		return fmt.Errorf("open the store: %w", err)
	}
	defer func() {
		// Logged rather than returned: the caller is already on its way out, and
		// a close failure must not replace the reason it exited.
		if err := sandboxStore.Cleanup(); err != nil {
			cfg.Logger.ErrorContext(ctx, "closing the store failed", "err", err)
		}
	}()

	// Start returns immediately; it must be called before the blocking
	// server.Start below or the loop would only begin once serving had ended.
	reconciler.NewReconciler(cfg.Logger, sandboxStore, sandboxRuntime).Start(ctx, cfg.ReconcilerInterval)

	// Start owns the listening log: it is the only place that knows the bind
	// succeeded and which port the kernel assigned.
	server := httpapi.NewAPI(cfg.Host, cfg.Port, cfg.Logger, sandboxStore, sandboxRuntime)
	if err := server.Start(ctx.Done()); err != nil {
		return fmt.Errorf("run the control plane: %w", err)
	}
	return nil
}
