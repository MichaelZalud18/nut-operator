/*
Copyright 2026 Michael Zalud.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package main

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/nodeagent"
)

var version = "dev"

const defaultSignalPath = "/run/power-agent/shutdown.json"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-h", "--help", "help":
			_, _ = fmt.Fprintln(os.Stdout, "Usage: power-signal-writer [--help|--version]")
			_, _ = fmt.Fprintln(os.Stdout, "Writes a structured node shutdown signal for the local node actuator.")
			return
		case "--version", "version":
			_, _ = fmt.Fprintln(os.Stdout, version)
			return
		default:
			_, _ = fmt.Fprintf(os.Stderr, "unknown argument %q\n", os.Args[1])
			os.Exit(64)
		}
	}

	logger := log.New(os.Stdout, "power-signal-writer ", log.LstdFlags|log.LUTC)
	config := signalWriterConfig{
		ExecutionID:        env("POWER_EXECUTION_ID", ""),
		Mode:               env("POWER_AGENT_MODE", "DryRun"),
		NodeName:           env("POWER_NODE_NAME", ""),
		PlanConfigHash:     env("POWER_PLAN_CONFIG_HASH", ""),
		Reason:             env("POWER_SIGNAL_REASON", "upsmon-fsd"),
		SelectedUPSDevices: splitCSV(env("POWER_SELECTED_UPS_DEVICES", "")),
		ShutdownFlow:       env("POWER_SHUTDOWN_FLOW", "upsmon-local"),
		SignalPath:         env("POWER_SIGNAL_PATH", defaultSignalPath),
		SignalTTL:          parseDuration(env("POWER_SIGNAL_TTL", "2m"), 2*time.Minute),
	}
	payload, reused, err := writeSignal(config, time.Now().UTC())
	if err != nil {
		logger.Printf("failed signalPath=%s node=%s error=%v", config.SignalPath, config.NodeName, err)
		os.Exit(1)
	}
	if reused {
		logger.Printf("reused active signal executionID=%s node=%s signalPath=%s", payload.ExecutionID, payload.NodeName, config.SignalPath)
		return
	}
	logger.Printf("wrote signal executionID=%s node=%s signalPath=%s dryRun=%t", payload.ExecutionID, payload.NodeName, config.SignalPath, payload.DryRun)
}

type signalWriterConfig struct {
	ExecutionID        string
	Mode               string
	NodeName           string
	PlanConfigHash     string
	Reason             string
	SelectedUPSDevices []string
	ShutdownFlow       string
	SignalPath         string
	SignalTTL          time.Duration
}

func writeSignal(config signalWriterConfig, now time.Time) (nodeagent.ShutdownSignal, bool, error) {
	if config.NodeName == "" {
		return nodeagent.ShutdownSignal{}, false, fmt.Errorf("POWER_NODE_NAME is required")
	}
	if config.PlanConfigHash == "" {
		return nodeagent.ShutdownSignal{}, false, fmt.Errorf("POWER_PLAN_CONFIG_HASH is required")
	}
	if config.ShutdownFlow == "" {
		return nodeagent.ShutdownSignal{}, false, fmt.Errorf("POWER_SHUTDOWN_FLOW is required")
	}
	if config.SignalPath == "" {
		config.SignalPath = defaultSignalPath
	}
	config.SelectedUPSDevices = canonicalStrings(config.SelectedUPSDevices)

	status := nodeagent.InspectSignal(config.SignalPath, config.SignalTTL, now, config.NodeName)
	if status.Active && reusableSignal(status.Payload, config) {
		return status.Payload, true, nil
	}
	if status.Reason == "SignalWrongNode" {
		return nodeagent.ShutdownSignal{}, false, fmt.Errorf("refusing to overwrite signal for node %q", status.Payload.NodeName)
	}

	executionID := config.ExecutionID
	if executionID == "" {
		executionID = fmt.Sprintf("upsmon-%s-%d", config.NodeName, now.UnixNano())
	}
	payload := nodeagent.ShutdownSignal{
		DryRun:             config.Mode != "Actuate",
		ExecutionID:        executionID,
		NodeName:           config.NodeName,
		PlanConfigHash:     config.PlanConfigHash,
		Reason:             config.Reason,
		SelectedUPSDevices: append([]string(nil), config.SelectedUPSDevices...),
		ShutdownFlow:       config.ShutdownFlow,
		Timestamp:          now.UTC().Format(time.RFC3339Nano),
	}
	if err := nodeagent.WriteSignalAtomic(config.SignalPath, payload); err != nil {
		return nodeagent.ShutdownSignal{}, false, err
	}
	return payload, false, nil
}

func reusableSignal(payload nodeagent.ShutdownSignal, config signalWriterConfig) bool {
	return payload.DryRun == (config.Mode != "Actuate") &&
		payload.NodeName == config.NodeName &&
		payload.PlanConfigHash == config.PlanConfigHash &&
		payload.Reason == config.Reason &&
		payload.ShutdownFlow == config.ShutdownFlow &&
		equalStrings(canonicalStrings(payload.SelectedUPSDevices), config.SelectedUPSDevices)
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
	return fallback
}

func splitCSV(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func canonicalStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	deduped := out[:0]
	var previous string
	for _, value := range out {
		if value == "" || value == previous {
			continue
		}
		deduped = append(deduped, value)
		previous = value
	}
	return deduped
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
