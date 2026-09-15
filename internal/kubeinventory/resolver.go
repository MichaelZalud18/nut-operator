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

package kubeinventory

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/capability"
	"github.com/MichaelZalud18/nut-operator/internal/inventory"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const declarativeInventorySourceID = "power.zalud.io/v1alpha1/declarative-inventory"

func declarativeStructuralInputs(ctx context.Context, reader client.Reader) (resolver.StructuralInputs, []resolver.Diagnostic, error) {
	var diagnostics []resolver.Diagnostic
	snapshot := inventory.Snapshot{
		SourceID: declarativeInventorySourceID,
	}
	profiles := capability.BundledProfiles()

	var upsDevices powerv1alpha1.UPSDeviceList
	if err := reader.List(ctx, &upsDevices); err != nil {
		return resolver.StructuralInputs{}, nil, fmt.Errorf("list UPSDevice resources: %w", err)
	}
	for i := range upsDevices.Items {
		obj := &upsDevices.Items[i]
		if result := ValidateUPSDevice(obj); !result.Accepted {
			diagnostics = append(diagnostics, resolverDiagnosticFromValidation(
				resolver.DiagnosticSourceInventory,
				"UPSDevice",
				obj.Name,
				result,
			))
			continue
		}
		snapshot.Entities = append(snapshot.Entities, inventoryEntityFromUPSDevice(obj))
	}

	var infrastructure powerv1alpha1.PowerInfrastructureList
	if err := reader.List(ctx, &infrastructure); err != nil {
		return resolver.StructuralInputs{}, nil, fmt.Errorf("list PowerInfrastructure resources: %w", err)
	}
	for i := range infrastructure.Items {
		obj := &infrastructure.Items[i]
		if result := ValidatePowerInfrastructure(obj); !result.Accepted {
			diagnostics = append(diagnostics, resolverDiagnosticFromValidation(
				resolver.DiagnosticSourceInventory,
				"PowerInfrastructure",
				obj.Name,
				result,
			))
			continue
		}
		snapshot.Entities = append(snapshot.Entities, inventoryEntityFromPowerInfrastructure(obj))
	}

	var nodes powerv1alpha1.PowerInventoryNodeList
	if err := reader.List(ctx, &nodes); err != nil {
		return resolver.StructuralInputs{}, nil, fmt.Errorf("list PowerInventoryNode resources: %w", err)
	}
	for i := range nodes.Items {
		obj := &nodes.Items[i]
		if result := ValidatePowerInventoryNode(obj); !result.Accepted {
			diagnostics = append(diagnostics, resolverDiagnosticFromValidation(
				resolver.DiagnosticSourceInventory,
				"PowerInventoryNode",
				obj.Name,
				result,
			))
			continue
		}
		snapshot.Entities = append(snapshot.Entities, inventoryEntityFromPowerInventoryNode(obj))
	}

	var edges powerv1alpha1.PowerInventoryEdgeList
	if err := reader.List(ctx, &edges); err != nil {
		return resolver.StructuralInputs{}, nil, fmt.Errorf("list PowerInventoryEdge resources: %w", err)
	}
	for i := range edges.Items {
		obj := &edges.Items[i]
		if result := ValidatePowerInventoryEdge(obj); !result.Accepted {
			diagnostics = append(diagnostics, resolverDiagnosticFromValidation(
				resolver.DiagnosticSourceInventory,
				"PowerInventoryEdge",
				obj.Name,
				result,
			))
			continue
		}
		snapshot.Edges = append(snapshot.Edges, inventoryEdgeFromPowerInventoryEdge(obj))
	}

	clusterNodes, agentCoverage, err := clusterNodeContext(ctx, reader)
	if err != nil {
		return resolver.StructuralInputs{}, nil, err
	}
	diagnostics = append(diagnostics, unmatchedInventoryNodeDiagnostics(snapshot, clusterNodes)...)

	var capabilityProfiles powerv1alpha1.UPSCapabilityProfileList
	if err := reader.List(ctx, &capabilityProfiles); err != nil {
		return resolver.StructuralInputs{}, nil, fmt.Errorf("list UPSCapabilityProfile resources: %w", err)
	}
	for i := range capabilityProfiles.Items {
		obj := &capabilityProfiles.Items[i]
		if result := ValidateUPSCapabilityProfile(obj); !result.Accepted {
			diagnostics = append(diagnostics, resolverDiagnosticFromValidation(
				resolver.DiagnosticSourceCapability,
				"UPSCapabilityProfile",
				obj.Name,
				result,
			))
			continue
		}
		profiles = append(profiles, UPSCapabilityProfile(obj))
	}

	if resolverDiagnosticsHaveErrors(diagnostics) {
		return resolver.StructuralInputs{}, diagnostics, resolver.ErrRejected
	}

	return resolver.StructuralInputs{
		SourceID:      declarativeInventorySourceID,
		Inventory:     snapshot,
		Profiles:      profiles,
		ClusterNodes:  clusterNodes,
		AgentCoverage: agentCoverage,
	}, diagnostics, nil
}

// clusterNodeContext reads the live cluster facts shutdown planning needs to
// name a node: which nodes exist with which labels, and which nodes each
// NodePowerAgent has selected.
//
// This is an external read, so it belongs here rather than in the resolver,
// which stays pure. Neither list participates in the bundle hash — node labels
// change for reasons that have nothing to do with shutdown planning, and only
// the membership actually derived from them belongs in plan identity.
func clusterNodeContext(ctx context.Context, reader client.Reader) ([]resolver.ClusterNode, []resolver.AgentCoverage, error) {
	var nodes corev1.NodeList
	if err := reader.List(ctx, &nodes); err != nil {
		return nil, nil, fmt.Errorf("list Node resources: %w", err)
	}
	clusterNodes := make([]resolver.ClusterNode, 0, len(nodes.Items))
	for i := range nodes.Items {
		node := &nodes.Items[i]
		clusterNodes = append(clusterNodes, resolver.ClusterNode{
			Name:   node.Name,
			Labels: node.Labels,
		})
	}

	var agents powerv1alpha1.NodePowerAgentList
	if err := reader.List(ctx, &agents); err != nil {
		return nil, nil, fmt.Errorf("list NodePowerAgent resources: %w", err)
	}
	coverage := make([]resolver.AgentCoverage, 0, len(agents.Items))
	for i := range agents.Items {
		agent := &agents.Items[i]
		coverage = append(coverage, resolver.AgentCoverage{
			Name:  agent.Name,
			Nodes: agent.Status.SelectedNodes,
		})
	}

	return clusterNodes, coverage, nil
}

// unmatchedInventoryNodeDiagnostics reports declared inventory nodes that do not
// exist in the cluster (IN-13).
//
// Node identity is trusted by convention: a PowerInventoryNode names a node and
// nothing previously checked that the name resolves. A typo therefore produced a
// power domain covering a node that cannot be shut down, discovered during an
// outage. This is a warning rather than a rejection because inventory is
// legitimately authored ahead of the hardware it describes — the point is that
// the gap is stated, not that it blocks.
func unmatchedInventoryNodeDiagnostics(snapshot inventory.Snapshot, clusterNodes []resolver.ClusterNode) []resolver.Diagnostic {
	if len(clusterNodes) == 0 {
		return nil
	}
	existing := make(map[string]struct{}, len(clusterNodes))
	for _, node := range clusterNodes {
		existing[node.Name] = struct{}{}
	}

	var diagnostics []resolver.Diagnostic
	for _, entity := range snapshot.Entities {
		if entity.Kind != inventory.EntityKindNode {
			continue
		}
		if _, found := existing[entity.ID]; found {
			continue
		}
		diagnostics = append(diagnostics, resolver.Diagnostic{
			Severity: resolver.DiagnosticWarning,
			Source:   resolver.DiagnosticSourceInventory,
			Reason:   "InventoryNodeNotInCluster",
			Subject:  entity.ID,
			Message: fmt.Sprintf("PowerInventoryNode %q names a node that does not exist in this cluster; "+
				"shutdown planning cannot reach it", entity.ID),
		})
	}
	return diagnostics
}

// ResolveDeviceCapabilityMatch matches one UPSDevice against the same profile
// set the structural resolver uses. Telemetry polling needs the matched
// profile before any ShutdownFlow compiles, so this is a device-scoped lookup
// rather than a second copy of the matching rules.
func ResolveDeviceCapabilityMatch(ctx context.Context, reader client.Reader, device *powerv1alpha1.UPSDevice) (capability.MatchResult, []capability.Diagnostic, error) {
	profiles := capability.BundledProfiles()

	var capabilityProfiles powerv1alpha1.UPSCapabilityProfileList
	if err := reader.List(ctx, &capabilityProfiles); err != nil {
		return capability.MatchResult{}, nil, fmt.Errorf("list UPSCapabilityProfile resources: %w", err)
	}
	for i := range capabilityProfiles.Items {
		obj := &capabilityProfiles.Items[i]
		if result := ValidateUPSCapabilityProfile(obj); !result.Accepted {
			continue
		}
		profiles = append(profiles, UPSCapabilityProfile(obj))
	}

	entity := inventoryEntityFromUPSDevice(device)
	match, diagnostics, err := capability.Match(capability.Device{
		ID:           entity.ID,
		Model:        entity.Model,
		Firmware:     entity.Firmware,
		DriverFamily: entity.DriverFamily,
	}, profiles)
	if err != nil {
		return capability.MatchResult{}, nil, err
	}
	return match, diagnostics, nil
}

func resolverDiagnosticFromValidation(source, kind, name string, result ValidationResult) resolver.Diagnostic {
	return resolver.Diagnostic{
		Severity: resolver.DiagnosticError,
		Source:   source,
		Reason:   result.Reason,
		Subject:  fmt.Sprintf("%s/%s", kind, name),
		Message:  result.Message,
	}
}

func resolverDiagnosticsHaveErrors(diagnostics []resolver.Diagnostic) bool {
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == resolver.DiagnosticError {
			return true
		}
	}
	return false
}

// ResolveStructuralBundle reads inventory and resolves a compile-time snapshot.
// Reader freshness is supplied by the caller; this is not execution-time evidence.
func ResolveStructuralBundle(ctx context.Context, reader client.Reader) (resolver.StructuralBundle, []resolver.Diagnostic, error) {
	inputs, diagnostics, err := declarativeStructuralInputs(ctx, reader)
	if err != nil {
		return resolver.StructuralBundle{}, diagnostics, err
	}
	bundle, resolved, err := resolver.ResolveStructural(inputs)
	return bundle, append(diagnostics, resolved...), err
}
