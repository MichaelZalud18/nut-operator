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

package controller

import (
	"context"
	"slices"
	"sort"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/adaptive"
	"github.com/MichaelZalud18/nut-operator/internal/executor"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
	"github.com/MichaelZalud18/nut-operator/internal/shutdownflow"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func communicationBudgetDevices(flow *power.ShutdownFlow, bundle resolver.StructuralBundle) ([]string, bool) {
	if flow == nil {
		return nil, false
	}
	input := resolver.AttachResolvedInputHash(planner.StructuralInputs{}, bundle)
	membership := map[string][]string{}
	for _, entry := range shutdownflow.PlannerGroupNodes(flow, bundle) {
		membership[entry.Group] = append(slices.Clone(entry.Acts), entry.Releases...)
	}
	var consumers []string
	for _, step := range flow.Status.CompiledSteps {
		nodes := membership[step.ID]
		if len(nodes) == 0 {
			// A node-less action may use the shared API or NUT path. Until that
			// coverage is more specific, budget against all modeled carriers.
			for _, dependency := range input.CommunicationDependencies {
				consumers = append(consumers, dependency.Dependent)
			}
			break
		}
		consumers = append(consumers, nodes...)
	}
	sort.Strings(consumers)
	return planner.CommunicationSupplyDevices(input, slices.Compact(consumers))
}

// The full compiled plan defines a conservative supply envelope for this run.
// Read fresh telemetry at every wave; PL-31/32 turn unavailable data into an
// unknown budget, not a failed flow or an assumed recovery.
func (r *ShutdownFlowReconciler) powerObserverForFlow(flow *power.ShutdownFlow, bundle resolver.StructuralBundle, selected []string, fallback adaptive.PowerObservation) executor.PowerObserver {
	supplies, unknown := communicationBudgetDevices(flow, bundle)
	names := append(slices.Clone(selected), supplies...)
	sort.Strings(names)
	names = slices.Compact(names)
	if len(names) == 0 && !unknown {
		return nil
	}
	return func(ctx context.Context) (adaptive.PowerObservation, error) {
		return r.runtimeBudgetObservation(ctx, names, bundle, fallback, unknown), nil
	}
}

func (r *ShutdownFlowReconciler) runtimeBudgetObservation(ctx context.Context, names []string, bundle resolver.StructuralBundle, fallback adaptive.PowerObservation, unknownSupply bool) adaptive.PowerObservation {
	var devices []power.UPSDevice
	for _, name := range names {
		device := power.UPSDevice{ObjectMeta: metav1.ObjectMeta{Name: name}}
		if err := r.Get(ctx, client.ObjectKey{Name: name}, &device); err != nil {
			device.Status = power.UPSDeviceStatus{Phase: power.UPSDevicePhaseUnknown}
		}
		devices = append(devices, device)
	}
	return runtimeBudgetFromDevices(devices, bundle, fallback, unknownSupply)
}

func runtimeBudgetFromDevices(devices []power.UPSDevice, bundle resolver.StructuralBundle, fallback adaptive.PowerObservation, unknownSupply bool) adaptive.PowerObservation {
	var active []power.UPSDevice
	var constrained []string
	unknown := unknownSupply
	for _, device := range devices {
		if device.Status.Phase == power.UPSDevicePhaseOnline {
			continue
		}
		active = append(active, device)
		constrained = append(constrained, device.Name)
		if !devicePhaseReportsPower(device.Status.Phase) {
			unknown = true
		}
	}
	observation := powerObservationFromDevices(active, runtimeIsTrustedForFlow(bundle.CapabilityMatches, constrained))
	if unknown {
		observation.RuntimeSeconds = nil
		observation.RuntimeTrusted = false
		// Partial reads are not proof that every required supply recovered.
		observation.OnBattery = observation.OnBattery || fallback.OnBattery
		observation.LowBattery = observation.LowBattery || fallback.LowBattery
	}
	return observation
}
