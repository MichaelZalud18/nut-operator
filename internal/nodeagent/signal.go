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

// Package nodeagent contains the file-level contract shared by the local
// upsmon handoff writer and the host actuator.
package nodeagent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// DeliveryChannelMarker is the file the operator always projects alongside the per-node signals.
//
// Without it, an operator that never created the signal Secret and an operator with nothing to say
// look identical to the actuator: kubelet mounts an empty directory either way, so the directory
// stats fine, no signal file is found, and readiness passes on a channel that can never deliver
// (F-86). The marker makes the projection itself provable — absent means the volume is not carrying
// the operator's Secret, which after OD-37 is the only path a halt can arrive on.
//
// Deliberately not suffixed `.json`: signal keys are `<node>.json`, so nothing a node can be named
// collides with this.
const DeliveryChannelMarker = "delivery-channel"

// ShutdownSignal is the structured handoff consumed by node actuators.
//
// There is deliberately no `dryRun` field (F-56). It looked like authorization and carried no
// information: the local writer set it from its own POWER_AGENT_MODE and the actuator then gated on
// it, so the actuator was reading its own configuration back out of a file, and the executor wrote a
// hard-coded false on the only path that survives OD-37. Whether this node may actually halt is the
// actuator's own env and policy, which no writer can reach.
type ShutdownSignal struct {
	ExecutionID        string   `json:"executionID"`
	NodeName           string   `json:"nodeName"`
	PlanConfigHash     string   `json:"planConfigHash"`
	Reason             string   `json:"reason"`
	SelectedUPSDevices []string `json:"selectedUPSDevices"`
	ShutdownFlow       string   `json:"shutdownFlow"`
	Timestamp          string   `json:"timestamp"`
}

// SignalStatus summarizes a signal-file inspection.
type SignalStatus struct {
	Active  bool
	Reason  string
	Key     string
	Path    string
	Payload ShutdownSignal
}

// InspectSignal validates a signal file against TTL and node binding.
func InspectSignal(path string, ttl time.Duration, now time.Time, expectedNode string) SignalStatus {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return SignalStatus{Path: path, Reason: "SignalMissing"}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return SignalStatus{Path: path, Reason: "SignalUnreadable"}
	}
	var payload ShutdownSignal
	if err := json.Unmarshal(data, &payload); err != nil {
		return SignalStatus{Path: path, Reason: "SignalInvalidJSON"}
	}
	if payload.ExecutionID == "" || payload.NodeName == "" || payload.PlanConfigHash == "" || payload.ShutdownFlow == "" || payload.Timestamp == "" {
		return SignalStatus{Path: path, Reason: "SignalMissingRequiredFields"}
	}
	if expectedNode != "" && payload.NodeName != expectedNode {
		return SignalStatus{Path: path, Reason: "SignalWrongNode", Payload: payload}
	}
	timestamp, err := time.Parse(time.RFC3339Nano, payload.Timestamp)
	if err != nil {
		return SignalStatus{Path: path, Reason: "SignalInvalidTimestamp", Payload: payload}
	}
	if ttl > 0 {
		if now.Sub(timestamp) > ttl {
			return SignalStatus{Path: path, Reason: "SignalStale", Payload: payload}
		}
		if timestamp.Sub(now) > ttl {
			return SignalStatus{Path: path, Reason: "SignalFromFuture", Payload: payload}
		}
	}
	return SignalStatus{
		Active:  true,
		Reason:  "SignalAccepted",
		Key:     SignalKey(payload),
		Path:    path,
		Payload: payload,
	}
}

// SignalKey identifies one actuator-worthy signal episode.
func SignalKey(payload ShutdownSignal) string {
	return payload.ExecutionID + ":" + payload.NodeName + ":" + payload.PlanConfigHash + ":" + payload.Timestamp
}

// WriteSignalAtomic writes the signal without exposing partial JSON to the actuator.
func WriteSignalAtomic(path string, payload ShutdownSignal) error {
	if path == "" {
		return fmt.Errorf("signal path is required")
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0750); err != nil {
		return fmt.Errorf("create signal directory %q: %w", dir, err)
	}
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("encode signal payload: %w", err)
	}
	data = append(data, '\n')

	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary signal file: %w", err)
	}
	tempName := temp.Name()
	defer func() {
		_ = os.Remove(tempName)
	}()

	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write temporary signal file: %w", err)
	}
	if err := temp.Chmod(0640); err != nil {
		_ = temp.Close()
		return fmt.Errorf("chmod temporary signal file: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary signal file: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("publish signal file: %w", err)
	}
	return nil
}
