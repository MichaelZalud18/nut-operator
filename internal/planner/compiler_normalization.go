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

package planner

import (
	"fmt"
	"sort"
)

func prepareStructuralInputs(structural StructuralInputs) (StructuralInputs, []Diagnostic, error) {
	normalized, err := normalizeStructuralInputs(structural)
	if err != nil {
		return StructuralInputs{}, []Diagnostic{{
			Severity: DiagnosticError,
			Reason:   "TriggerEncodingFailed",
			Message:  fmt.Sprintf("triggers could not be hashed for deterministic ordering: %v", err),
		}}, ErrRejected
	}
	if normalized.ExecutionScope != nil && len(normalized.ExecutionScope.UPSDevices) == 0 {
		return StructuralInputs{}, []Diagnostic{{Severity: DiagnosticError, Reason: "ExecutionScopeEmpty", Message: "an execution scope must name eligible UPS devices"}}, ErrRejected
	}
	return normalized, nil, nil
}

func normalizeStructuralInputs(input StructuralInputs) (StructuralInputs, error) {
	normalized := StructuralInputs{
		SourceID:          input.SourceID,
		ObservedAt:        input.ObservedAt,
		ResolvedInputHash: input.ResolvedInputHash,
		TierPolicy:        normalizeTierPolicy(input.TierPolicy),
		AbortBehavior:     input.AbortBehavior,
		AbortNotify:       input.AbortNotify,
		TierOverrunPolicy: input.TierOverrunPolicy,
		Triggers:          append([]Trigger(nil), input.Triggers...),
		Groups:            append([]Group(nil), input.Groups...),
		Steps:             append([]Step(nil), input.Steps...),

		DeviceCapabilities:        append([]DeviceCapability(nil), input.DeviceCapabilities...),
		PowerDomains:              append([]PowerDomainMembership(nil), input.PowerDomains...),
		CommunicationDependencies: normalizeCommunicationDependencies(input.CommunicationDependencies),
		CommunicationServices:     normalizeCommunicationServices(input.CommunicationServices),
		CommunicationExemptNodes:  sortedUnique(input.CommunicationExemptNodes),
		InventoryEntities:         sortedUnique(input.InventoryEntities),
		GroupNodes:                append([]GroupNodeMembership(nil), input.GroupNodes...),
		NodeTiers:                 append([]NodeTier(nil), input.NodeTiers...),
		ControlPlaneNodes:         append([]ControlPlaneNode(nil), input.ControlPlaneNodes...),
		HookDigests:               append([]HookDigest(nil), input.HookDigests...),
	}
	if input.ExecutionScope != nil {
		normalized.ExecutionScope = &ExecutionScope{UPSDevices: sortedUnique(input.ExecutionScope.UPSDevices)}
	}
	sort.Slice(normalized.ControlPlaneNodes, func(i, j int) bool {
		return normalized.ControlPlaneNodes[i].Name < normalized.ControlPlaneNodes[j].Name
	})
	sort.SliceStable(normalized.NodeTiers, func(left, right int) bool {
		return normalized.NodeTiers[left].Name < normalized.NodeTiers[right].Name
	})
	for i := range normalized.GroupNodes {
		normalized.GroupNodes[i].Acts = append([]string(nil), normalized.GroupNodes[i].Acts...)
		normalized.GroupNodes[i].Releases = append([]string(nil), normalized.GroupNodes[i].Releases...)
		sort.Strings(normalized.GroupNodes[i].Acts)
		sort.Strings(normalized.GroupNodes[i].Releases)
	}
	sort.SliceStable(normalized.GroupNodes, func(left, right int) bool {
		return normalized.GroupNodes[left].Group < normalized.GroupNodes[right].Group
	})
	for i := range normalized.DeviceCapabilities {
		normalized.DeviceCapabilities[i].TelemetryVariables = append([]string(nil), normalized.DeviceCapabilities[i].TelemetryVariables...)
		sort.Strings(normalized.DeviceCapabilities[i].TelemetryVariables)
	}
	sort.SliceStable(normalized.DeviceCapabilities, func(left, right int) bool {
		return normalized.DeviceCapabilities[left].DeviceID < normalized.DeviceCapabilities[right].DeviceID
	})
	for i := range normalized.PowerDomains {
		normalized.PowerDomains[i].UPSDevices = append([]string(nil), normalized.PowerDomains[i].UPSDevices...)
		normalized.PowerDomains[i].Members = append([]string(nil), normalized.PowerDomains[i].Members...)
		normalized.PowerDomains[i].Nodes = append([]string(nil), normalized.PowerDomains[i].Nodes...)
		normalized.PowerDomains[i].Infrastructure = append([]string(nil), normalized.PowerDomains[i].Infrastructure...)
		sort.Strings(normalized.PowerDomains[i].UPSDevices)
		sort.Strings(normalized.PowerDomains[i].Members)
		sort.Strings(normalized.PowerDomains[i].Nodes)
		sort.Strings(normalized.PowerDomains[i].Infrastructure)
	}
	sort.SliceStable(normalized.PowerDomains, func(left, right int) bool {
		return normalized.PowerDomains[left].Name < normalized.PowerDomains[right].Name
	})
	sort.SliceStable(normalized.HookDigests, func(left, right int) bool {
		if normalized.HookDigests[left].Namespace == normalized.HookDigests[right].Namespace {
			return normalized.HookDigests[left].Name < normalized.HookDigests[right].Name
		}
		return normalized.HookDigests[left].Namespace < normalized.HookDigests[right].Namespace
	})
	for i := range normalized.Triggers {
		normalized.Triggers[i].UPSDevices = append([]string(nil), normalized.Triggers[i].UPSDevices...)
		normalized.Triggers[i].PowerDomains = append([]string(nil), normalized.Triggers[i].PowerDomains...)
		sort.Strings(normalized.Triggers[i].UPSDevices)
		sort.Strings(normalized.Triggers[i].PowerDomains)
	}
	for i := range normalized.Groups {
		normalized.Groups[i].Requires = append([]string(nil), normalized.Groups[i].Requires...)
		normalized.Groups[i].Before = append([]string(nil), normalized.Groups[i].Before...)
		normalized.Groups[i].After = append([]string(nil), normalized.Groups[i].After...)
		normalized.Groups[i].ShutdownTier = copyInt32Ptr(normalized.Groups[i].ShutdownTier)
		normalized.Groups[i].HookRef = copyHookReference(normalized.Groups[i].HookRef)
		normalized.Groups[i].Params = copyStringMap(normalized.Groups[i].Params)
		sort.Strings(normalized.Groups[i].Requires)
		sort.Strings(normalized.Groups[i].Before)
		sort.Strings(normalized.Groups[i].After)
	}
	for i := range normalized.Steps {
		normalized.Steps[i].HookRef = copyHookReference(normalized.Steps[i].HookRef)
		normalized.Steps[i].Params = copyStringMap(normalized.Steps[i].Params)
	}
	if err := sortTriggers(normalized.Triggers); err != nil {
		return StructuralInputs{}, err
	}
	sortGroups(normalized.Groups)
	return normalized, nil
}

func copyHookReference(input *HookReference) *HookReference {
	if input == nil {
		return nil
	}
	output := *input
	return &output
}

func copyStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}

// sortTriggers orders triggers deterministically by content hash. Each trigger's hash is computed
// once before sorting, keeping JSON marshaling and hashing to O(n) work.
func sortTriggers(triggers []Trigger) error {
	type hashedTrigger struct {
		trigger Trigger
		hash    string
	}
	decorated := make([]hashedTrigger, len(triggers))
	for i, trigger := range triggers {
		hash, err := stableHash(trigger)
		if err != nil {
			return fmt.Errorf("hash trigger %d for deterministic ordering: %w", i, err)
		}
		decorated[i] = hashedTrigger{trigger: trigger, hash: hash}
	}
	sort.SliceStable(decorated, func(i, j int) bool {
		return decorated[i].hash < decorated[j].hash
	})
	for i, d := range decorated {
		triggers[i] = d.trigger
	}
	return nil
}

func sortGroups(groups []Group) {
	sort.SliceStable(groups, func(i, j int) bool {
		return groups[i].Name < groups[j].Name
	})
}
