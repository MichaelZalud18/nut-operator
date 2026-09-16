// Command nut-driver-supervisor owns foreground NUT driver processes.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/nutsupervisor"
)

var version = "dev"

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		fmt.Println(version)
		return
	}
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "Usage: nut-driver-supervisor [--version]")
		os.Exit(2)
	}
	opts, err := options(os.Getenv)
	if err == nil {
		ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
		defer cancel()
		err = nutsupervisor.Run(ctx, opts)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func options(getenv func(string) string) (nutsupervisor.Options, error) {
	opts := nutsupervisor.Options{ConfigDir: getenv("NUT_CONFPATH"), Interval: 5 * time.Second}
	if opts.ConfigDir == "" {
		opts.ConfigDir = "/etc/nut"
	}
	if value := getenv("NUT_SUPERVISOR_INTERVAL_SECONDS"); value != "" {
		seconds, err := strconv.ParseUint(value, 10, 64)
		if err != nil || seconds == 0 || seconds > uint64((1<<63-1)/time.Second) {
			return opts, fmt.Errorf("NUT_SUPERVISOR_INTERVAL_SECONDS must be a positive integer duration in seconds")
		}
		opts.Interval = time.Duration(seconds) * time.Second
	}
	return opts, nil
}
