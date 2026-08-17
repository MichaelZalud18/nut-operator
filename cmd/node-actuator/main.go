package main

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/nodeagent"
)

var version = "dev"

// Powering the node off is a single, fixed operation: the reboot(2) syscall with
// LINUX_REBOOT_CMD_POWER_OFF. There is deliberately no way to configure a different mechanism.
//
// An earlier build also accepted an arbitrary command (POWER_POWEROFF_COMMAND, defaulting to
// systemctl poweroff) but nothing could ever select it, so it was removed rather than exposed
// (F-36). Exposing it would have meant a CRD field that runs an operator-chosen executable as root
// on every node, and it would have widened the container's privileges: the syscall needs
// CAP_SYS_BOOT alone, which is what F-13 argued for, while shelling out to systemctl needs host
// PID/dbus access. Fewer privileges and no configurable command is the better trade for the one
// operation whose blast radius is the whole machine.
const (
	policyDisabled = "Disabled"
	policySimulate = "Simulate"
	policyPowerOff = "PowerOff"

	// modeActuate is the only agent mode under which a node may be halted. It is read from this
	// process's environment and never from a signal file (F-56).
	modeActuate = "Actuate"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-h", "--help", "help":
			_, _ = fmt.Fprintln(os.Stdout, "Usage: node-actuator [--help|--version|--ready]")
			_, _ = fmt.Fprintln(os.Stdout, "Watches POWER_SIGNAL_PATHS, validates structured shutdown signals, and performs the configured actuator policy.")
			return
		case "--version", "version":
			_, _ = fmt.Fprintln(os.Stdout, version)
			return
		case "--ready", "ready":
			// F-64: this is what the readiness probe runs, and unlike --version it can fail.
			//
			// It deliberately re-reads the environment rather than sharing state with the running
			// process: an exec probe is a separate process with the same environment, so the only
			// thing it can observe about the loop is what the loop wrote down.
			if err := checkActuatorReady(loadActuatorConfig(), time.Now().UTC()); err != nil {
				_, _ = fmt.Fprintf(os.Stderr, "not ready: %v\n", err)
				os.Exit(1)
			}
			_, _ = fmt.Fprintln(os.Stdout, "ready")
			return
		default:
			_, _ = fmt.Fprintf(os.Stderr, "unknown argument %q\n", os.Args[1])
			os.Exit(64)
		}
	}

	logger := log.New(os.Stdout, "node-actuator ", log.LstdFlags|log.LUTC)

	config := loadActuatorConfig()
	mode := config.Mode
	policy := config.Policy

	logger.Printf("starting mode=%s policy=%s node=%s signalPaths=%s signalTTL=%s poweroff=reboot-syscall(POWER_OFF)", config.Mode, config.Policy, config.NodeName, strings.Join(config.SignalPaths, ","), config.SignalTTL)

	switch policy {
	case policyDisabled, "":
		block(logger, "disabled policy")
	case policySimulate:
		watchSignals(logger, config, simulateActuator)
	case policyPowerOff:
		if mode != modeActuate {
			block(logger, "PowerOff requires POWER_AGENT_MODE=Actuate")
		}
		// F-61: prove the capability is held now, not during a power event.
		//
		// This is the configuration that claims it can halt a node, so it is the one place worth
		// refusing to start over. Every way of losing CAP_SYS_BOOT is silent, and the code that
		// would have noticed runs once, on a node under load, at the end of a UPS runtime.
		armed := newGateTrace(logger, config.NodeName, "")
		if err := verifySysBootAvailable(); err != nil {
			armed.fail(gateCapabilityPermitted, err.Error())
			logger.Printf("refusing to arm PowerOff actuation: %v", err)
			os.Exit(78)
		}
		armed.pass(gateCapabilityPermitted, "CAP_SYS_BOOT is in the permitted set; actuation armed")
		watchSignals(logger, config, powerOffActuator)
	default:
		logger.Printf("unknown actuator policy %q", policy)
		os.Exit(64)
	}
}

type actuatorConfig struct {
	Mode        string
	Policy      string
	NodeName    string
	SignalPaths []string
	SignalTTL   time.Duration
	Interval    time.Duration
	// ShutdownFlow is the flow this agent declares it belongs to, and the value an accepted
	// signal's own ShutdownFlow must equal (F-55). Empty means the agent declared none, and any
	// flow may release it.
	//
	// There is deliberately no PlanConfigHash counterpart. The agent's POWER_AGENT_CONFIG_HASH is a
	// hash of its own rendered config, not the planner's plan hash that the signal carries, so
	// comparing the two would reject every operator-issued signal -- see F-91.
	ShutdownFlow string
	// StatePath is where the watch loop records each completed pass, and the only thing the
	// readiness probe can observe about it (F-64).
	StatePath string
}

// loadActuatorConfig reads the actuator's configuration from the environment.
//
// Both the running process and the `--ready` probe go through here, so a probe cannot end up
// judging the loop against a different idea of what was configured.
func loadActuatorConfig() actuatorConfig {
	nodeName := env("POWER_NODE_NAME", "")
	// One variable, not two (F-75). POWER_SIGNAL_PATH and POWER_SIGNAL_PATHS both reached the
	// actuator and only the plural was read, so the singular was a knob that silently did nothing.
	// It survives on the upsmon side, where it is the local writer's target and does have an effect.
	return actuatorConfig{
		Mode:         env("POWER_AGENT_MODE", "DryRun"),
		Policy:       env("POWER_ACTUATOR_POLICY", policySimulate),
		NodeName:     nodeName,
		SignalPaths:  signalPaths(env("POWER_SIGNAL_PATHS", ""), defaultSignalPath(nodeName)),
		SignalTTL:    parseDuration(env("POWER_SIGNAL_TTL", "2m"), 2*time.Minute),
		Interval:     parseDuration(env("POWER_ACTUATOR_INTERVAL", "5s"), 5*time.Second),
		ShutdownFlow: env("POWER_SHUTDOWN_FLOW", ""),
		StatePath:    env("POWER_ACTUATOR_STATE_PATH", "/run/actuator/state.json"),
	}
}

type actuatorFunc func(*log.Logger, actuatorConfig, nodeagent.SignalStatus) error

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

// defaultSignalPath is where the operator's projected Secret lands, derived rather than fixed.
//
// The old default was the local tmpfs path the upsmon container writes, which meant a failure to
// inject POWER_SIGNAL_PATH did not disarm the actuator -- it repointed it at the one path OD-37
// declines to give authority. Defaults fail safe here: with no node name there is nothing to build
// a per-node path from, so the actuator watches nothing rather than guessing.
func defaultSignalPath(nodeName string) string {
	if nodeName == "" {
		return ""
	}
	return "/var/lib/power-agent/signals/" + nodeName + ".json"
}

func signalPaths(value, fallback string) []string {
	if value == "" {
		if fallback == "" {
			return nil
		}
		return []string{fallback}
	}
	// Not ':' (F-75). A colon is a legal character in a path, and splitting on it fragmented any
	// such path into pieces that do not exist -- each failing as SignalMissing, the one reason
	// watchSignals deliberately never logs. The failure was silent by construction.
	fields := strings.FieldsFunc(value, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\t' || r == ' '
	})
	out := make([]string, 0, len(fields))
	seen := map[string]struct{}{}
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" {
			continue
		}
		if _, exists := seen[field]; exists {
			continue
		}
		seen[field] = struct{}{}
		out = append(out, field)
	}
	if len(out) == 0 && fallback != "" {
		return []string{fallback}
	}
	return out
}

func block(logger *log.Logger, reason string) {
	logger.Printf("blocking forever: %s", reason)
	select {}
}

// scanSignalPaths runs one pass over the configured signal paths, actuating at most once.
//
// Split out of watchSignals so the at-most-once property is reachable by a test: the loop around it
// never returns, and a rule nothing can exercise is a rule that quietly stops holding.
//
// `seen` is updated in place, which is what carries the actuated keys into the state file the caller
// writes at the end of each pass (F-58).
// It returns the paths whose rejection was a clock disagreement rather than an ordinary one, which
// is what readiness reports on (F-59).
func scanSignalPaths(logger *log.Logger, config actuatorConfig, actuate actuatorFunc, seen map[string]struct{}) []string {
	var clockSkewed []string
	for _, path := range config.SignalPaths {
		status := nodeagent.InspectSignal(path, config.SignalTTL, time.Now().UTC(), config.NodeName)
		if !status.Active {
			// SignalFromFuture is the one rejection that cannot be explained by anything but the
			// two clocks disagreeing (F-59). The executor stamps the signal at write time, so a
			// timestamp more than a whole TTL ahead of this node means this node's clock is behind
			// the operator's by more than the delivery window -- a standing fault, not an event.
			//
			// SignalStale deliberately does not count. A signal ages out for ordinary reasons too:
			// nobody collected it, or the flow ended. Treating that as a clock fault would report
			// skew every time a shutdown was called off.
			if status.Reason == "SignalFromFuture" {
				clockSkewed = append(clockSkewed, path)
			}
			// SignalMissing is the normal case on every tick and is deliberately not logged --
			// including as a gate. A gate line per path per tick would bury the burst the trace
			// exists to produce, and "nothing has been asked of this node" is not a broken link.
			if status.Reason != "SignalMissing" {
				newGateTrace(logger, status.Payload.NodeName, status.Payload.ExecutionID).
					fail(gateSignalAccepted, status.Reason+" at "+status.Path)
				logger.Printf("ignoring shutdown signal path=%s reason=%s", status.Path, status.Reason)
			}
			continue
		}
		trace := newGateTrace(logger, status.Payload.NodeName, status.Payload.ExecutionID)
		trace.pass(gateSignalAccepted, "read, parsed, bound to this node, and inside its TTL, from "+status.Path)
		// Checked by value, not merely for presence (F-55). InspectSignal already binds the signal
		// to this node; this binds it to the flow the agent declares it belongs to, so a second
		// flow's release cannot halt a node that was never enrolled in it.
		//
		// Empty means the agent set no spec.shutdownFlowRef and any flow may release it. That is
		// the operator's existing model -- a flow names its agents through AgentRefs, and the
		// agent's own reference back is optional -- so an unset ref is permission, not an omission.
		if config.ShutdownFlow != "" && status.Payload.ShutdownFlow != config.ShutdownFlow {
			trace.fail(gateFlowBinding, "signal names flow "+status.Payload.ShutdownFlow+
				" but this agent is enrolled in "+config.ShutdownFlow)
			logger.Printf("ignoring shutdown signal path=%s reason=SignalWrongFlow executionID=%s flow=%s expectedFlow=%s",
				status.Path, status.Payload.ExecutionID, status.Payload.ShutdownFlow, config.ShutdownFlow)
			continue
		}
		if config.ShutdownFlow == "" {
			trace.pass(gateFlowBinding, "this agent declares no shutdownFlowRef, so any flow may release it")
		} else {
			trace.pass(gateFlowBinding, "signal flow matches this agent's "+config.ShutdownFlow)
		}
		if _, alreadySeen := seen[status.Key]; !alreadySeen {
			seen[status.Key] = struct{}{}
			if err := actuate(logger, config, status); err != nil {
				logger.Printf("actuator rejected signal path=%s executionID=%s node=%s reason=%s error=%v", status.Path, status.Payload.ExecutionID, status.Payload.NodeName, status.Reason, err)
			}
		}
		// One live signal ends the pass (F-88). SignalKey is built from the payload, so two paths
		// describing one episode produce two keys and `seen` does not connect them -- continuing
		// past an active signal is how one shutdown became two actuations. OD-37 removes the second
		// source, and this removes the shape that let it matter.
		return clockSkewed
	}
	return clockSkewed
}

func watchSignals(logger *log.Logger, config actuatorConfig, actuate actuatorFunc) {
	ticker := time.NewTicker(config.Interval)
	defer ticker.Stop()
	// Seeded from the state file so a container restart cannot re-actuate (F-58). `seen` is
	// in-memory and the emptyDir holding the signal is pod-scoped, so an actuator that restarted
	// while a still-fresh signal sat on disk used to act on it a second time. The same volume that
	// makes readiness able to fail carries the keys across the restart.
	seen := map[string]struct{}{}
	if previous, err := readActuatorState(config.StatePath); err == nil {
		for _, key := range previous.ActuatedKeys {
			seen[key] = struct{}{}
		}
		if len(previous.ActuatedKeys) > 0 {
			logger.Printf("restored %d already-actuated signal key(s) from %s", len(previous.ActuatedKeys), config.StatePath)
		}
	}
	// Logged on transition only, so a persistently unwritable state volume does not bury the
	// actuation log under one line per interval.
	stateWriteFailed := false
	// Same discipline for the delivery channel, and the same reason. It is the first link in the
	// halt chain and the only one that can be broken for months without anything asking it to do
	// something -- so it reports itself as soon as it is known, and again whenever it changes.
	channel := channelTrace{trace: newGateTrace(logger, config.NodeName, "")}

	for {
		clockSkewed := scanSignalPaths(logger, config, actuate, seen)

		// Record the completed pass (F-64). This is the only evidence readiness has that the loop
		// is running at all, so a failure to write it must be visible rather than swallowed -- a
		// silently unwritten state file would read as a stalled loop, which is the correct
		// conclusion but for the wrong reason, and the log line is what separates the two.
		unreadable := unreadableSignalDirs(config.SignalPaths)
		unprojected := unprojectedSignalDirs(config.SignalPaths)
		channel.observe(unreadable, unprojected)
		state := actuatorState{
			Timestamp:             time.Now().UTC().Format(time.RFC3339),
			Policy:                config.Policy,
			IntervalSeconds:       config.Interval.Seconds(),
			UnreadableSignalDirs:  unreadable,
			UnprojectedSignalDirs: unprojected,
			ClockSkewedSignals:    clockSkewed,
			ActuatedKeys:          actuatedKeys(seen),
		}
		if err := writeActuatorState(config.StatePath, state); err != nil {
			if !stateWriteFailed {
				stateWriteFailed = true
				logger.Printf("cannot record watch-loop state at %s, so readiness will fail: %v", config.StatePath, err)
			}
		} else if stateWriteFailed {
			stateWriteFailed = false
			logger.Printf("recording watch-loop state at %s again", config.StatePath)
		}

		<-ticker.C
	}
}

func simulateActuator(logger *log.Logger, config actuatorConfig, status nodeagent.SignalStatus) error {
	logger.Printf("simulate actuator accepted shutdown signal executionID=%s node=%s flow=%s mode=%s", status.Payload.ExecutionID, status.Payload.NodeName, status.Payload.ShutdownFlow, config.Mode)
	return nil
}

func powerOffActuator(logger *log.Logger, config actuatorConfig, status nodeagent.SignalStatus) error {
	trace := newGateTrace(logger, status.Payload.NodeName, status.Payload.ExecutionID)
	// Read from this process's own env, never from the signal (F-56). main() already refuses to
	// start this policy outside Actuate, so reaching here in another mode should be impossible --
	// which is exactly why it is worth checking on the one operation that cannot be undone.
	if config.Mode != modeActuate {
		trace.fail(gateModeAuthorized, "POWER_AGENT_MODE is "+config.Mode+"; only "+modeActuate+" may halt a node")
		logger.Printf("refusing to power off in mode=%s: only %s may halt a node, executionID=%s node=%s",
			config.Mode, modeActuate, status.Payload.ExecutionID, status.Payload.NodeName)
		return nil
	}
	trace.pass(gateModeAuthorized, "POWER_AGENT_MODE="+modeActuate+" from this process's own environment")
	logger.Printf("poweroff actuator executing poweroff executionID=%s node=%s flow=%s", status.Payload.ExecutionID, status.Payload.NodeName, status.Payload.ShutdownFlow)
	return runPoweroff(logger, status.Payload)
}

// syncTimeout bounds the flush.
//
// unix.Sync() has no timeout of its own, and a hung mount -- stalled NFS, a dead iSCSI target, a
// failing disk -- blocks it indefinitely. SkipSync cannot rescue that case: the executor decides
// the skip before the sync starts, so a node that meets a sick mount mid-flush has no way to change
// its mind. Unbounded, the node simply never halts and the UPS runs down with it still up.
//
// A node that halts dirty beats a node that never halts, so the bound is generous rather than tight
// -- long enough that a healthy flush on a busy node is never cut short, short enough that a wedged
// one does not consume the runtime the whole plan is spending.
var syncTimeout = 30 * time.Second

// runPoweroff flushes dirty page cache, then powers the machine off. Despite the syscall's name,
// reboot(2) with LINUX_REBOOT_CMD_POWER_OFF halts and cuts power; it does not restart.
//
// The sync is timed on every call and the duration logged, because that number is the only direct
// measurement of the handoff tail anyone has. OD-27 currently reserves 20% of runtime for it as a
// guess, and this is the cheapest real evidence available for settling that.
func runPoweroff(logger *log.Logger, payload nodeagent.ShutdownSignal) error {
	trace := newGateTrace(logger, payload.NodeName, payload.ExecutionID)
	if payload.SkipSync {
		// Loud, never silent. A node that skipped the flush comes back with more to recover, and
		// the operator reading the log afterwards needs to know that was chosen rather than failed.
		trace.pass(gateSync, "skipped: the executor reported the plan overrunning")
		logger.Printf("poweroff actuator SKIPPING sync before halt: the executor reported the plan overrunning, so dirty page cache is being dropped to save time; expect more filesystem recovery on this node executionID=%s node=%s",
			payload.ExecutionID, payload.NodeName)
	} else {
		started := time.Now()
		done := make(chan struct{})
		// Not waited on beyond the select. If Sync() is wedged on a mount this goroutine never
		// returns, and that is fine: reboot(2) is a few lines below and takes the process with it.
		go func() {
			syncFilesystems()
			close(done)
		}()
		// Logged before the wait, not after it. A trace that only records completed flushes cannot
		// distinguish a sync that hung from a sync that was never reached, and those two point at
		// different halves of the system.
		trace.pass(gateSync, "started, bounded at "+syncTimeout.String())
		select {
		case <-done:
			trace.pass(gateSync, "completed in "+roundedDuration(time.Since(started)))
			logger.Printf("poweroff actuator sync completed in %s executionID=%s node=%s",
				time.Since(started).Round(time.Millisecond), payload.ExecutionID, payload.NodeName)
		case <-time.After(syncTimeout):
			// Surfaced, not swallowed. A flush that outlasts this is evidence of a sick mount, and
			// it is evidence that would otherwise be lost with the machine.
			trace.fail(gateSync, "did not finish within "+syncTimeout.String()+" and was cut short; halting dirty")
			logger.Printf("poweroff actuator sync did NOT finish within %s and was cut short; halting with dirty pages still in cache. This usually means a hung mount (stalled NFS, dead iSCSI target, failing disk) -- check this node's mounts before returning it to service. executionID=%s node=%s",
				syncTimeout, payload.ExecutionID, payload.NodeName)
		}
	}
	if err := raiseHaltCapability(); err != nil {
		trace.fail(gateCapabilityEffective, err.Error())
		return fmt.Errorf("raise CAP_SYS_BOOT before halting: %w", err)
	}
	trace.pass(gateCapabilityEffective, "CAP_SYS_BOOT moved from permitted into effective")
	// The last line this process writes on a working actuate path. Anything after it is either an
	// error from the syscall or -- the case with no other check available -- a container that is not
	// really in the host PID namespace, where reboot(2) returns success and the machine stays up.
	trace.pass(gateSyscallIssued, "reboot(2) LINUX_REBOOT_CMD_POWER_OFF; no further output is expected from this container")
	if err := rebootPoweroff(); err != nil {
		trace.fail(gateSyscallIssued, err.Error())
		return fmt.Errorf("run reboot poweroff syscall: %w", err)
	}
	return nil
}
