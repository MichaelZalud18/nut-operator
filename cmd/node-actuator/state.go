package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/nodeagent"
)

// actuatorState is the actuator's own record that its watch loop ran, and what it found when it did.
//
// It exists because readiness had no way to fail (F-64). The probe ran `--version`, which prints a
// string and returns before any configuration is parsed, so it passed on a process parked forever in
// block() and on one whose signal directory had never been readable. `F-46` retired the same
// instruction on the server image for the same reason: a check that cannot fail is worse than no
// check, because it reads as coverage.
//
// The file is written to a volume mounted only into the actuator, deliberately not to the emptyDir
// it shares with the signal writer (F-57). Health evidence that the neighbouring container can
// rewrite is not evidence.
type actuatorState struct {
	// Timestamp is when the watch loop last completed a pass, in RFC3339.
	Timestamp string `json:"timestamp"`
	// Policy is the policy that loop is running under, carried for the failure message.
	Policy string `json:"policy"`
	// IntervalSeconds is the loop's configured period, which is what makes a staleness bound
	// derivable rather than guessed.
	IntervalSeconds float64 `json:"intervalSeconds"`
	// UnreadableSignalDirs lists configured signal directories that could not be stat'd on that
	// pass. A missing signal *file* is the normal case and is not recorded here; a missing
	// directory means the volume never arrived.
	UnreadableSignalDirs []string `json:"unreadableSignalDirs,omitempty"`
	// UnprojectedSignalDirs lists signal directories that exist but carry no delivery-channel
	// marker (F-86). Distinct from unreadable on purpose: the directory is there and readable, it
	// just is not the operator's Secret, which is the case an empty mount and a working-but-quiet
	// channel used to share.
	UnprojectedSignalDirs []string `json:"unprojectedSignalDirs,omitempty"`
	// ClockSkewedSignals lists signal paths rejected because their timestamp was more than a whole
	// TTL ahead of this node's clock (F-59). The executor stamps the signal when it writes it, so
	// that can only mean the two clocks disagree by more than the delivery window -- and once they
	// do, every operator-issued signal to this node is rejected, silently, until someone reads a
	// container log.
	ClockSkewedSignals []string `json:"clockSkewedSignals,omitempty"`
	// ActuatedKeys are the signal keys this actuator has already acted on (F-58). Kept here rather
	// than only in memory because the map and the signal file have different lifetimes: `seen` dies
	// with the container, the emptyDir holding the signal lives as long as the pod, so a restart
	// used to find a still-fresh signal it had already acted on and act again.
	//
	// Bounded by pruneActuatedKeys, because the key includes the signal timestamp and would
	// otherwise gain an entry per distinct signal for the pod's whole life (F-75).
	ActuatedKeys []string `json:"actuatedKeys,omitempty"`
}

// maxActuatedKeys bounds the retained history.
//
// Large enough that no realistic flow re-actuates something it already handled -- a node receives
// one signal per execution, and an execution is a rare event -- and small enough that the file stays
// a small write on every tick.
const maxActuatedKeys = 64

// actuatedKeys renders the seen set deterministically and bounded.
func actuatedKeys(seen map[string]struct{}) []string {
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return pruneActuatedKeys(keys)
}

// pruneActuatedKeys keeps the newest keys when the set outgrows its bound.
//
// Signal keys carry the timestamp they were issued with, so lexical order is chronological for the
// RFC3339 stamps InspectSignal validates, and dropping from the front drops the oldest.
func pruneActuatedKeys(keys []string) []string {
	if len(keys) <= maxActuatedKeys {
		return keys
	}
	return keys[len(keys)-maxActuatedKeys:]
}

// writeActuatorState records a completed watch-loop pass.
//
// Written to a temporary file and renamed, so a probe reading concurrently sees either the previous
// pass or this one and never a half-written file.
func writeActuatorState(path string, state actuatorState) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode actuator state: %w", err)
	}
	temporary := path + ".tmp"
	if err := os.WriteFile(temporary, encoded, 0o600); err != nil {
		return fmt.Errorf("write actuator state: %w", err)
	}
	if err := os.Rename(temporary, path); err != nil {
		return fmt.Errorf("replace actuator state: %w", err)
	}
	return nil
}

func readActuatorState(path string) (actuatorState, error) {
	var state actuatorState
	raw, err := os.ReadFile(path)
	if err != nil {
		return state, err
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		return state, fmt.Errorf("parse actuator state: %w", err)
	}
	return state, nil
}

// unreadableSignalDirs returns the configured signal directories that cannot be stat'd.
func unreadableSignalDirs(paths []string) []string {
	var unreadable []string
	for _, path := range paths {
		directory := filepath.Dir(path)
		if _, err := os.Stat(directory); err != nil {
			unreadable = append(unreadable, directory)
		}
	}
	return unreadable
}

// unprojectedSignalDirs returns signal directories that carry no delivery-channel marker.
//
// The marker is a key the operator always writes into the signal Secret, so its absence means the
// directory is not that Secret: either the volume is an empty stand-in kubelet mounted for a Secret
// that does not exist, or something else is mounted there. A directory that cannot be stat'd at all
// is unreadableSignalDirs' case and is deliberately not repeated here.
func unprojectedSignalDirs(paths []string) []string {
	var unprojected []string
	for _, path := range paths {
		directory := filepath.Dir(path)
		if _, err := os.Stat(directory); err != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join(directory, nodeagent.DeliveryChannelMarker)); err != nil {
			unprojected = append(unprojected, directory)
		}
	}
	return unprojected
}

// actuatorStaleAfter is how old a recorded pass may be before readiness fails.
//
// Derived from the loop's own interval rather than fixed, so a deployment that slows the loop down
// does not start failing readiness for running exactly as configured. The floor keeps a very short
// interval from making the check hair-triggered against ordinary scheduling latency.
func actuatorStaleAfter(interval time.Duration) time.Duration {
	bound := 3 * interval
	if bound < 30*time.Second {
		return 30 * time.Second
	}
	return bound
}

// checkActuatorReady reports whether the actuator is doing what its configuration says it should.
//
// Under a Disabled policy that means blocking, and there is no loop to have run -- the exec of this
// binary is itself the evidence that the container is alive, which is all readiness can mean for a
// process whose configured job is to do nothing. Under any other policy the watch loop must have
// completed a pass recently and must have been able to see its signal directories.
func checkActuatorReady(config actuatorConfig, now time.Time) error {
	if config.Policy == policyDisabled || config.Policy == "" {
		return nil
	}

	state, err := readActuatorState(config.StatePath)
	if err != nil {
		return fmt.Errorf("the %s watch loop has not recorded a pass at %s: %w", config.Policy, config.StatePath, err)
	}
	if len(state.UnreadableSignalDirs) > 0 {
		return fmt.Errorf("signal directories unreadable on the last pass: %v", state.UnreadableSignalDirs)
	}
	if len(state.UnprojectedSignalDirs) > 0 {
		return fmt.Errorf("signal directories carry no %q marker, so the operator's delivery channel is not mounted there: %v",
			nodeagent.DeliveryChannelMarker, state.UnprojectedSignalDirs)
	}
	// F-59. Reported through readiness because that is the channel this node already has: the
	// actuator holds no credentials and publishes nothing upward (F-60), while pod readiness
	// already reaches the operator through status.nodeStatuses. A node whose clock rejects every
	// signal the operator sends is not covered, and saying so is the whole point.
	if len(state.ClockSkewedSignals) > 0 {
		return fmt.Errorf("signals were timestamped more than one TTL ahead of this node's clock, so every operator-issued signal is being rejected; check time sync on this node: %v",
			state.ClockSkewedSignals)
	}

	recorded, err := time.Parse(time.RFC3339, state.Timestamp)
	if err != nil {
		return fmt.Errorf("actuator state carries an unparseable timestamp %q: %w", state.Timestamp, err)
	}
	interval := time.Duration(state.IntervalSeconds * float64(time.Second))
	staleAfter := actuatorStaleAfter(interval)
	if age := now.Sub(recorded); age > staleAfter {
		return fmt.Errorf("the %s watch loop last completed a pass %s ago, more than the %s allowed for a %s interval",
			config.Policy, age.Truncate(time.Second), staleAfter, interval)
	}
	return nil
}
