package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/nickstrad/quickspin/internal/cli"
	"github.com/nickstrad/quickspin/internal/runtime"
)

func main() {
	logLevel := new(slog.LevelVar)
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: logLevel,
	}))

	if err := run(logger, logLevel); err != nil {
		logger.Error("quickspin failed", "component", "quickspin", "err", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger, logLevel *slog.LevelVar) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rt, err := runtime.NewDockerRuntime(nil, logger.With(
		"component", "runtime",
		"backend", "docker",
	))
	if err != nil {
		return err
	}

	return cli.NewCommand(
		rt,
		logger.With("component", "cli"),
		logLevel,
	).ExecuteContext(ctx)
}
