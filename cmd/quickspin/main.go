package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/nickstrad/quickspin/internal/cli"
	"github.com/nickstrad/quickspin/internal/runtime"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	rt, err := runtime.NewDockerRuntime(nil)
	if err != nil {
		return err
	}

	return cli.NewCommand(rt).ExecuteContext(ctx)
}
