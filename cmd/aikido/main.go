// aikido is a CLI for the aikido library: chat one-shots and agent runs over
// an in-memory VFS, backed by OpenRouter.
//
// Per ADR-010 a CLI is a v2 deliverable; this binary is a v1.x convenience.
// All command logic lives in github.com/govengers/aikido/internal/cli; main.go
// only wires .env loading, signal handling, and dispatch.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/govengers/aikido/internal/cli"
)

// exitInterrupted is the shell convention for a process ended by SIGINT.
const exitInterrupted = 130

func main() {
	os.Exit(run())
}

// run returns the exit code, so its deferred cleanup runs before os.Exit.
func run() int {
	if err := cli.LoadDotEnv(".env"); err != nil {
		fmt.Fprintln(os.Stderr, "aikido: ignoring .env:", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	app := cli.NewApp(cli.NewOpenRouterClient)
	if err := app.Run(ctx, os.Args); err != nil {
		if errors.Is(err, context.Canceled) {
			fmt.Fprintln(os.Stderr, "interrupted")
			return exitInterrupted
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}
