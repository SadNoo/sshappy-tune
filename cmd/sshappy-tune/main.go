package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/SadNoo/sshappy-tune/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(cli.New(os.Stdout, os.Stderr).Run(ctx, os.Args[1:]))
}
