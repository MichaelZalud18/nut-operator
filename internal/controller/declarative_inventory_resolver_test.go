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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/capability"
	"github.com/MichaelZalud18/nut-operator/internal/inventory"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
)

func TestResolveDeclarativeStructuralBundleUsesBundledProfiles(t *testing.T) {
	scheme := clusterContextScheme(t)

	reader := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			&powerv1alpha1.UPSDevice{
				ObjectMeta: objectMeta("tower"),
				Spec: powerv1alpha1.UPSDeviceSpec{
					Driver: "dummy-ups",
					Identity: powerv1alpha1.UPSDeviceIdentitySpec{
						Model: "TOWER_1000VA_230V",
					},
					PowerDomains: []string{"rack-a"},
				},
			},
			&powerv1alpha1.PowerInventoryNode{
				ObjectMeta: objectMeta("node-a"),
				Spec: powerv1alpha1.PowerInventoryNodeSpec{
					NodeName:                "node-a",
					CommunicationPathExempt: boolPtr(true),
				},
			},
			&powerv1alpha1.PowerInventoryEdge{
				ObjectMeta: objectMeta("tower-feeds-node-a"),
				Spec: powerv1alpha1.PowerInventoryEdgeSpec{
					From: powerv1alpha1.PowerInventoryEntityReference{
						Kind: powerv1alpha1.PowerInventoryEntityUPSDevice,
						Name: "tower",
					},
					To: powerv1alpha1.PowerInventoryEntityReference{
						Kind: powerv1alpha1.PowerInventoryEntityNode,
						Name: "node-a",
					},
					Relation: powerv1alpha1.PowerInventoryEdgeFeeds,
					Input:    "psu-a",
				},
			},
		).
		Build()

	bundle, diagnostics, err := resolveDeclarativeStructuralBundle(context.Background(), reader)
	if err != nil {
		t.Fatalf("expected bundled profile resolution to succeed, got %v with diagnostics %#v", err, diagnostics)
	}
	if len(bundle.CapabilityMatches) != 1 {
		t.Fatalf("expected one capability match, got %#v", bundle.CapabilityMatches)
	}
	match := bundle.CapabilityMatches[0]
	if match.ProfileID != capability.BundledUbiquitiUPSTowerProfileID {
		t.Fatalf("expected bundled Ubiquiti tower profile, got %#v", match)
	}
	if match.ProfileSource != capability.ProfileSourceBundled {
		t.Fatalf("expected bundled source, got %#v", match)
	}
}

func TestResolveDeclarativeStructuralBundleMapsLastDitchRoleToTierOne(t *testing.T) {
	scheme := clusterContextScheme(t)
	trueValue := true
	reader := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(&powerv1alpha1.PowerInventoryNode{
			ObjectMeta: objectMeta("control-plane-a"),
			Spec: powerv1alpha1.PowerInventoryNodeSpec{
				NodeName:                "control-plane-a",
				PowerPlanningExempt:     &trueValue,
				CommunicationPathExempt: &trueValue,
				Roles: powerv1alpha1.PowerInventoryNodeRoles{
					LastDitchRole: "control-plane",
				},
			},
		}).
		Build()

	bundle, diagnostics, err := resolveDeclarativeStructuralBundle(context.Background(), reader)
	if err != nil {
		t.Fatalf("expected inventory resolution to succeed, got %v with diagnostics %#v", err, diagnostics)
	}
	if len(bundle.Topology.Entities) != 1 {
		t.Fatalf("expected one entity, got %#v", bundle.Topology.Entities)
	}
	tier := bundle.Topology.Entities[0].ShutdownTier
	if tier == nil || *tier != 1 {
		t.Fatalf("expected lastDitchRole to map to shutdown tier 1, got %#v", tier)
	}
}

func TestDeclarativeStructuralInputsLeaveSnapshotObservedAtEmpty(t *testing.T) {
	reader := fake.NewClientBuilder().
		WithScheme(clusterContextScheme(t)).
		Build()

	inputs, diagnostics, err := declarativeStructuralInputs(context.Background(), reader)
	if err != nil {
		t.Fatalf("expected declarative inputs to resolve, got %v with diagnostics %#v", err, diagnostics)
	}
	if inputs.Inventory.ObservedAt != "" {
		t.Fatalf("declarative inventory must not restamp observedAt on each resolve, got %q", inputs.Inventory.ObservedAt)
	}
}

func objectMeta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name}
}

// clusterContextScheme registers both the power API and core types, since node
// context reads corev1.Node alongside the operator's own resources.
func clusterContextScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := powerv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add power API to scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core API to scheme: %v", err)
	}
	return scheme
}

func TestClusterNodeContextReadsNodesAndAgentCoverage(t *testing.T) {
	agent := &powerv1alpha1.NodePowerAgent{ObjectMeta: objectMeta("rack-a-agent")}
	agent.Status.SelectedNodes = []string{"node-a2", "node-a1"}

	reader := fake.NewClientBuilder().
		WithScheme(clusterContextScheme(t)).
		WithObjects(
			&corev1.Node{ObjectMeta: metav1.ObjectMeta{
				Name:   "node-a1",
				Labels: map[string]string{"rack": "a"},
			}},
			&corev1.Node{ObjectMeta: metav1.ObjectMeta{
				Name:   "node-a2",
				Labels: map[string]string{"rack": "a"},
			}},
			agent,
		).
		Build()

	nodes, coverage, err := clusterNodeContext(context.Background(), reader)
	if err != nil {
		t.Fatalf("clusterNodeContext returned error: %v", err)
	}
	if len(nodes) != 2 {
		t.Fatalf("expected both nodes, got %#v", nodes)
	}
	if nodes[0].Labels["rack"] != "a" {
		t.Fatalf("expected node labels to carry through, got %#v", nodes[0])
	}
	if len(coverage) != 1 || coverage[0].Name != "rack-a-agent" || len(coverage[0].Nodes) != 2 {
		t.Fatalf("expected agent coverage for both nodes, got %#v", coverage)
	}
}

// IN-13: a PowerInventoryNode names a node by convention and nothing used to
// check the name resolved. A typo produced a power domain covering a node that
// could never be shut down, discovered during an outage.
func TestUnmatchedInventoryNodeDiagnostics(t *testing.T) {
	snapshot := inventory.Snapshot{
		Entities: []inventory.Entity{
			{ID: "node-a1", Kind: inventory.EntityKindNode},
			{ID: "node-typo", Kind: inventory.EntityKindNode},
			{ID: "tower", Kind: inventory.EntityKindUPSDevice},
		},
	}
	clusterNodes := []resolver.ClusterNode{{Name: "node-a1"}}

	diagnostics := unmatchedInventoryNodeDiagnostics(snapshot, clusterNodes)
	if len(diagnostics) != 1 {
		t.Fatalf("expected exactly the unmatched node to be reported, got %#v", diagnostics)
	}
	if diagnostics[0].Subject != "node-typo" || diagnostics[0].Reason != "InventoryNodeNotInCluster" {
		t.Fatalf("unexpected diagnostic %#v", diagnostics[0])
	}
	if diagnostics[0].Severity != resolver.DiagnosticWarning {
		t.Fatalf("inventory may legitimately precede hardware, so this warns; got %q", diagnostics[0].Severity)
	}
}

// With no visible nodes there is nothing to check against, and claiming every
// inventory node is missing would be worse than saying nothing.
func TestUnmatchedInventoryNodeDiagnosticsStaySilentWithoutClusterNodes(t *testing.T) {
	snapshot := inventory.Snapshot{
		Entities: []inventory.Entity{{ID: "node-a1", Kind: inventory.EntityKindNode}},
	}
	if diagnostics := unmatchedInventoryNodeDiagnostics(snapshot, nil); diagnostics != nil {
		t.Fatalf("expected no diagnostics without cluster nodes, got %#v", diagnostics)
	}
}
