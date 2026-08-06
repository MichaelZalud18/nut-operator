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

package resolver

import (
	"sort"

	"github.com/MichaelZalud18/nut-operator/internal/planner"
)

// AttachResolvedInputHash marks planner structural input with the resolver
// bundle hash so inventory and capability changes participate in plan identity,
// and carries the resolved capability matches and derived power domains the
// planner needs to validate trigger definitions (PL-19).
func AttachResolvedInputHash(flow planner.StructuralInputs, bundle StructuralBundle) planner.StructuralInputs {
	flow.ResolvedInputHash = bundle.Hash
	if flow.ObservedAt == "" {
		flow.ObservedAt = bundle.ObservedAt
	}
	flow.DeviceCapabilities = plannerDeviceCapabilities(bundle)
	flow.PowerDomains = plannerPowerDomains(bundle)
	return flow
}

func plannerDeviceCapabilities(bundle StructuralBundle) []planner.DeviceCapability {
	if len(bundle.CapabilityMatches) == 0 {
		return nil
	}
	capabilities := make([]planner.DeviceCapability, 0, len(bundle.CapabilityMatches))
	for _, match := range bundle.CapabilityMatches {
		capabilities = append(capabilities, planner.DeviceCapability{
			DeviceID:           match.DeviceID,
			ProfileID:          match.ProfileID,
			Unidentified:       match.Unidentified,
			TelemetryVariables: append([]string(nil), match.TelemetryVariables...),
		})
	}
	sort.SliceStable(capabilities, func(left, right int) bool {
		return capabilities[left].DeviceID < capabilities[right].DeviceID
	})
	return capabilities
}

func plannerPowerDomains(bundle StructuralBundle) []planner.PowerDomainMembership {
	if len(bundle.Topology.Domains) == 0 {
		return nil
	}
	domains := make([]planner.PowerDomainMembership, 0, len(bundle.Topology.Domains))
	for _, domain := range bundle.Topology.Domains {
		domains = append(domains, planner.PowerDomainMembership{
			Name:       domain.Name,
			UPSDevices: append([]string(nil), domain.UPSDevices...),
		})
	}
	sort.SliceStable(domains, func(left, right int) bool {
		return domains[left].Name < domains[right].Name
	})
	return domains
}
