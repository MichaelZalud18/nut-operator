package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"time"
)

var version = "dev"

const (
	policyDisabled        = "Disabled"
	policyStub            = "Stub"
	policySystemdPoweroff = "SystemdPoweroff"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-h", "--help", "help":
			fmt.Fprintln(os.Stdout, "Usage: node-actuator [--help|--version]")
			fmt.Fprintln(os.Stdout, "Watches POWER_SIGNAL_PATH and performs only fail-closed stub behavior unless a future actuator policy is implemented.")
			return
		case "--version", "version":
			fmt.Fprintln(os.Stdout, version)
			return
		default:
			fmt.Fprintf(os.Stderr, "unknown argument %q\n", os.Args[1])
			os.Exit(64)
		}
	}

	logger := log.New(os.Stdout, "node-actuator ", log.LstdFlags|log.LUTC)

	mode := env("POWER_AGENT_MODE", "DryRun")
	policy := env("POWER_ACTUATOR_POLICY", policyStub)
	signalPath := env("POWER_SIGNAL_PATH", "/run/power-agent/shutdown.signal")
	ttl := parseDuration(env("POWER_SIGNAL_TTL", "2m"), 2*time.Minute)
	interval := parseDuration(env("POWER_ACTUATOR_INTERVAL", "5s"), 5*time.Second)

	logger.Printf("starting mode=%s policy=%s signalPath=%s signalTTL=%s", mode, policy, signalPath, ttl)

	switch policy {
	case policyDisabled, "":
		block(logger, "disabled policy")
	case policyStub:
		watchStub(logger, signalPath, ttl, interval)
	case policySystemdPoweroff:
		logger.Printf("systemd poweroff policy is not implemented in this image")
		os.Exit(78)
	default:
		logger.Printf("unknown actuator policy %q", policy)
		os.Exit(64)
	}
}

func env(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func parseDuration(value string, fallback time.Duration) time.Duration {
	duration, err := time.ParseDuration(value)
	if err == nil {
		return duration
	}
	if seconds, convErr := strconv.ParseInt(value, 10, 64); convErr == nil {
		return time.Duration(seconds) * time.Second
	}
	return fallback
}

func block(logger *log.Logger, reason string) {
	logger.Printf("blocking forever: %s", reason)
	select {}
}

func watchStub(logger *log.Logger, signalPath string, ttl, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		if message, ok := activeSignal(signalPath, ttl); ok {
			logger.Printf("stub actuator observed active shutdown signal: %s", message)
		}
		<-ticker.C
	}
}

func activeSignal(path string, ttl time.Duration) (string, bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", false
	}
	if ttl > 0 && time.Since(info.ModTime()) > ttl {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Sprintf("path=%s", path), true
	}
	return fmt.Sprintf("path=%s bytes=%d", path, len(data)), true
}
