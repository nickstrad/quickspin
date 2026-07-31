package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/nickstrad/quickspin/internal/cli"
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

	// A nil API leaves the CLI to build an HTTP client from --server. Nothing
	// here opens a Docker connection: only `quickspin serve` needs one, and it
	// opens it itself.
	return cli.NewCommand(
		nil,
		logger.With("component", "cli"),
		logLevel,
	).ExecuteContext(ctx)
}
