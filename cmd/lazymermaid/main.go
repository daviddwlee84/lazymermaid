package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/daviddwlee84/lazymermaid/internal/app"
)

var version = "dev"

func main() {
	if version == "dev" {
		if b, ok := debug.ReadBuildInfo(); ok && b.Main.Version != "" && b.Main.Version != "(devel)" {
			version = b.Main.Version
		}
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Execute(ctx, os.Args[1:], version); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(app.ExitCode(err))
	}
}
