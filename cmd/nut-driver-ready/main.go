// Command nut-driver-ready checks configured drivers through the patched NUT CLI.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/MichaelZalud18/nut-operator/internal/nutreadiness"
)

var version = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "--version" {
		_, _ = fmt.Fprintln(stdout, version)
		return 0
	}
	if len(args) != 0 {
		_, _ = fmt.Fprintln(stderr, "Usage: nut-driver-ready [--version]")
		return 2
	}
	if err := nutreadiness.Check(ctx); err != nil {
		_, _ = fmt.Fprintf(stderr, "nut-driver-ready: %v\n", err)
		return 1
	}
	return 0
}
