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
	"testing"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/adaptive"
	"github.com/MichaelZalud18/nut-operator/internal/capability"
	"github.com/MichaelZalud18/nut-operator/internal/executor"
	"github.com/MichaelZalud18/nut-operator/internal/inventory"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
	"github.com/MichaelZalud18/nut-operator/internal/shutdownflow"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func communicationRuntimeFixture(t *testing.T) (*power.ShutdownFlow, resolver.StructuralBundle) {
	t.Helper()
	flow, bundle := communicationOrderFixture(t)
	bundle.Topology.Domains = []inventory.PowerDomain{
		{Name: "compute", UPSDevices: []string{"compute-ups"}, Nodes: []string{"consumer"}},
		{Name: "network", UPSDevices: []string{"network-ups"}, Nodes: []string{"carrier"}, Infrastructure: []string{"switch"}},
	}
	bundle.CapabilityMatches = []capability.MatchResult{dynamicRuntimeMatch("compute-ups"), dynamicRuntimeMatch("network-ups")}
	for i := range bundle.CapabilityMatches {
		bundle.CapabilityMatches[i].TelemetryVariables = []string{"ups.status", "battery.runtime"}
	}
	compiled := shutdownflow.CompileFlow(flow, bundle, power.PowerShutdownTierPolicySpec{})
	if compiled.ConfigHash == "" {
		t.Fatalf("fixture rejected: %+v", compiled.Diagnostics)
	}
	flow.Status.CompiledSteps = compiled.Steps
	flow.Status.CompiledWaves = compiled.Waves
	flow.Status.ConfigHash = compiled.ConfigHash
	return flow, bundle
}

func TestCommunicationRuntimeBudget(t *testing.T) {
	for _, tc := range []struct {
		name                              string
		compute, network                  power.UPSDevicePhase
		missing, untrusted, unknownSupply bool
		want                              *int64
		onBattery, lowBattery, trusted    bool
	}{
		{name: "short carrier", compute: power.UPSDevicePhaseOnBattery, network: power.UPSDevicePhaseOnBattery, want: adaptiveRuntimePtr(60), onBattery: true, trusted: true},
		{name: "online carrier excluded", compute: power.UPSDevicePhaseOnBattery, network: power.UPSDevicePhaseOnline, want: adaptiveRuntimePtr(900), onBattery: true, trusted: true, untrusted: true},
		{name: "carrier low battery", compute: power.UPSDevicePhaseOnBattery, network: power.UPSDevicePhaseLowBattery, want: adaptiveRuntimePtr(60), onBattery: true, lowBattery: true, trusted: true},
		{name: "carrier stale", compute: power.UPSDevicePhaseOnBattery, network: power.UPSDevicePhaseStale, onBattery: true},
		{name: "carrier unreadable", compute: power.UPSDevicePhaseOnBattery, missing: true, onBattery: true},
		{name: "untrusted carrier", compute: power.UPSDevicePhaseOnBattery, network: power.UPSDevicePhaseOnBattery, want: adaptiveRuntimePtr(60), onBattery: true, untrusted: true},
		{name: "compute recovered carrier failing", compute: power.UPSDevicePhaseOnline, network: power.UPSDevicePhaseOnBattery, want: adaptiveRuntimePtr(60), onBattery: true, trusted: true},
		{name: "all recovered", compute: power.UPSDevicePhaseOnline, network: power.UPSDevicePhaseOnline},
		{name: "partial recovery unknown compute", compute: power.UPSDevicePhaseUnknown, network: power.UPSDevicePhaseOnline, onBattery: true},
		{name: "unknown modeled supply", compute: power.UPSDevicePhaseOnBattery, network: power.UPSDevicePhaseOnline, unknownSupply: true, onBattery: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flow, bundle := communicationRuntimeFixture(t)
			if tc.unknownSupply {
				bundle.Topology.Domains = bundle.Topology.Domains[:1]
			}
			if tc.untrusted {
				bundle.CapabilityMatches = bundle.CapabilityMatches[:1]
			}
			compute := upsDeviceTelemetry("compute-ups", tc.compute, 900, 80, 20)
			network := upsDeviceTelemetry("network-ups", tc.network, 60, 20, 10)
			r := shutdownFlowReconcilerWithUPSDevices(t, compute, network)
			if tc.missing {
				if err := r.Delete(context.Background(), &network); err != nil {
					t.Fatal(err)
				}
			}
			selected := []string{"compute-ups"}
			observer := r.powerObserverForFlow(flow, bundle, selected, adaptive.PowerObservation{OnBattery: true})
			got, err := observer(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got.OnBattery != tc.onBattery || got.LowBattery != tc.lowBattery || got.RuntimeTrusted != tc.trusted || (got.RuntimeSeconds == nil) != (tc.want == nil) {
				t.Fatalf("observation=%+v", got)
			}
			if tc.want != nil && *got.RuntimeSeconds != *tc.want {
				t.Fatalf("runtime=%d want %d", *got.RuntimeSeconds, *tc.want)
			}
			if !slices.Equal(selected, []string{"compute-ups"}) {
				t.Fatal("budget changed trigger selection")
			}
			history := r.flowRuntimeObservation(context.Background(), flow, &power.ShutdownTriggerEvaluationStatus{SelectedUPSDevices: selected}, bundle)
			if tc.trusted && tc.want != nil {
				if history.RuntimeSeconds == nil || *history.RuntimeSeconds != *tc.want {
					t.Fatalf("warning disagrees with execution: %+v", history)
				}
			} else if history.RuntimeSeconds != nil {
				t.Fatal("warning trusted an unknown budget")
			}
		})
	}
}

func TestCommunicationBudgetRereadsCarrierAtEveryBoundary(t *testing.T) {
	flow, bundle := communicationRuntimeFixture(t)
	network := upsDeviceTelemetry("network-ups", power.UPSDevicePhaseOnBattery, 200, 20, 10)
	r := shutdownFlowReconcilerWithUPSDevices(t, upsDeviceTelemetry("compute-ups", power.UPSDevicePhaseOnBattery, 900, 80, 20), network)
	observer := r.powerObserverForFlow(flow, bundle, []string{"compute-ups"}, adaptive.PowerObservation{OnBattery: true})
	for _, seconds := range []int64{200, 25} {
		var device power.UPSDevice
		if err := r.Get(context.Background(), client.ObjectKey{Name: "network-ups"}, &device); err != nil {
			t.Fatal(err)
		}
		device.Status.RuntimeSeconds = &seconds
		if err := r.Status().Update(context.Background(), &device); err != nil {
			t.Fatal(err)
		}
		got, err := observer(context.Background())
		if err != nil || got.RuntimeSeconds == nil || *got.RuntimeSeconds != seconds {
			t.Fatalf("stale budget: %+v, %v", got, err)
		}
	}
	compiled := shutdownflow.CompileFlow(flow, bundle, power.PowerShutdownTierPolicySpec{})
	if compiled.ConfigHash != flow.Status.ConfigHash {
		t.Fatal("runtime observations changed structural identity")
	}
}

func TestCommunicationBudgetScopesToCompiledWork(t *testing.T) {
	flow, bundle := communicationRuntimeFixture(t)
	// An unrelated path must not constrain a plan with fully resolved targets.
	bundle.Topology.CommunicationOrders = append(bundle.Topology.CommunicationOrders, inventory.DerivedEdge{From: "unrelated", To: "other-switch"})
	bundle.Topology.Domains = append(bundle.Topology.Domains, inventory.PowerDomain{Name: "other", UPSDevices: []string{"other-ups"}, Infrastructure: []string{"other-switch"}})
	names, unknown := communicationBudgetDevices(flow, bundle)
	if unknown || !slices.Equal(names, []string{"network-ups"}) {
		t.Fatalf("wrong supplies: %v unknown=%v", names, unknown)
	}
	flow.Spec.Groups = append(flow.Spec.Groups, power.ShutdownGroup{Name: "shared-api", Action: power.ShutdownStepNotify})
	flow.Status.CompiledSteps = append(flow.Status.CompiledSteps, power.CompiledShutdownStep{ID: "shared-api"})
	names, unknown = communicationBudgetDevices(flow, bundle)
	if unknown || !slices.Equal(names, []string{"network-ups", "other-ups"}) {
		t.Fatalf("unresolved shared work lost conservative budget: %v unknown=%v", names, unknown)
	}
}

func TestCommunicationSupplyCompressesExecutorBudget(t *testing.T) {
	flow, bundle := communicationRuntimeFixture(t)
	flow.Spec.Groups[1].Action = power.ShutdownStepWait
	flow.Spec.Groups[1].Params = map[string]string{"duration": "100s"}
	flow.Spec.Groups[1].Timeout = &metav1.Duration{Duration: 100 * time.Second}
	compiled := shutdownflow.CompileFlow(flow, bundle, power.PowerShutdownTierPolicySpec{})
	if compiled.ConfigHash == "" {
		t.Fatalf("compile: %+v", compiled.Diagnostics)
	}
	flow.Status.CompiledSteps = compiled.Steps
	r := shutdownFlowReconcilerWithUPSDevices(t,
		upsDeviceTelemetry("compute-ups", power.UPSDevicePhaseOnBattery, 900, 80, 20),
		upsDeviceTelemetry("network-ups", power.UPSDevicePhaseOnBattery, 60, 20, 10))
	var slept []time.Duration
	e := executor.Executor{Writer: &fakeAuditStore{}, Clock: time.Now, NewID: func() string { return "communication-budget" },
		Observer: r.powerObserverForFlow(flow, bundle, []string{"compute-ups"}, adaptive.PowerObservation{OnBattery: true}),
		Sleep:    func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil },
	}
	_, err := e.Execute(context.Background(), executor.Input{ShutdownFlow: flow.Name, PlanConfigHash: compiled.ConfigHash, Mode: executor.ModeDryRun,
		Waves: executorWavesFromFlow(compiled.Waves, compiled.Steps), Groups: []executor.Group{
			{Name: "drain-consumer", Action: executor.ActionWait, WaitDuration: 100 * time.Second, Timeout: 100 * time.Second},
			{Name: "halt-consumer", Action: executor.ActionAgentShutdown}, {Name: "halt-carrier", Action: executor.ActionAgentShutdown},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(slept, []time.Duration{48 * time.Second}) {
		t.Fatalf("switch's 60s supply must constrain the 100s wait after 20%% reserve, got %v", slept)
	}
}

func TestPublishedCommunicationBudgetMatchesRuntimeSelection(t *testing.T) {
	for _, mode := range []string{"resolved", "unknown-supply", "node-less", "linear"} {
		t.Run(mode, func(t *testing.T) {
			flow, bundle := communicationRuntimeFixture(t)
			switch mode {
			case "unknown-supply":
				bundle.Topology.Domains = bundle.Topology.Domains[:1]
			case "node-less":
				flow.Spec.Groups = append(flow.Spec.Groups, power.ShutdownGroup{Name: "shared-api", Action: power.ShutdownStepNotify})
			case "linear":
				for _, index := range []int{1, 2, 0} {
					g := flow.Spec.Groups[index]
					flow.Spec.Steps = append(flow.Spec.Steps, power.ShutdownStep{ID: g.Name, Type: g.Action, Target: g.Target})
				}
				flow.Spec.Groups = nil
			}
			compiled := shutdownflow.CompileFlow(flow, bundle, power.PowerShutdownTierPolicySpec{})
			if compiled.Artifact == nil || compiled.Artifact.CommunicationBudget == nil {
				t.Fatalf("missing artifact: %+v", compiled.Diagnostics)
			}
			flow.Status.CompiledSteps = compiled.Steps
			names, unknown := communicationBudgetDevices(flow, bundle)
			published := compiled.Artifact.CommunicationBudget
			publishedUnknown := false
			for _, s := range published.Supplies {
				publishedUnknown = publishedUnknown || s.UnknownSupply
			}
			if !slices.Equal(names, published.UPSDevices) || unknown != publishedUnknown {
				t.Fatalf("runtime=%v/%v publication=%+v", names, unknown, published)
			}
			if mode == "node-less" && !slices.Equal(published.UnresolvedActions, []string{"shared-api"}) {
				t.Fatalf("missing conservative coverage reason: %+v", published)
			}
		})
	}
}
