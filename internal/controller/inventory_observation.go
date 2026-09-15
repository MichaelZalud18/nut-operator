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
	"errors"
	"strconv"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/capability"
	"github.com/MichaelZalud18/nut-operator/internal/inventory"
	"github.com/MichaelZalud18/nut-operator/internal/kubeinventory"
	"github.com/MichaelZalud18/nut-operator/internal/metrics"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// resolveDeclarativeStructuralBundle publishes observations for an inventory compilation.
func resolveDeclarativeStructuralBundle(ctx context.Context, reader client.Reader) (resolver.StructuralBundle, []resolver.Diagnostic, error) {
	bundle, diagnostics, err := kubeinventory.ResolveStructuralBundle(ctx, reader)
	recordInventoryCompileMetrics(bundle, diagnostics, err)
	if err == nil {
		for _, match := range bundle.CapabilityMatches {
			recordCapabilityMatchMetric(match, nil)
		}
	}
	return bundle, diagnostics, err
}

func recordInventoryCompileMetrics(bundle resolver.StructuralBundle, diagnostics []resolver.Diagnostic, err error) {
	result := "Accepted"
	if err != nil {
		result = "Failed"
		if errors.Is(err, resolver.ErrRejected) {
			result = "Rejected"
		}
	}
	metrics.InventoryCompileTotal.WithLabelValues(result).Inc()

	entities := map[inventory.EntityKind]int{
		inventory.EntityKindUPSDevice:           0,
		inventory.EntityKindNode:                0,
		inventory.EntityKindPowerInfrastructure: 0,
	}
	for _, entity := range bundle.Topology.Entities {
		entities[entity.Kind]++
	}
	for kind, count := range entities {
		metrics.InventoryEntities.WithLabelValues(string(kind)).Set(float64(count))
	}

	edges := map[inventory.EdgeRelation]int{
		inventory.EdgeRelationFeeds:   0,
		inventory.EdgeRelationCarries: 0,
	}
	for _, edge := range bundle.Topology.Edges {
		edges[edge.Relation]++
	}
	for relation, count := range edges {
		metrics.InventoryEdges.WithLabelValues(string(relation)).Set(float64(count))
	}

	metrics.InventoryPowerDomains.Set(float64(len(bundle.Topology.Domains)))
	metrics.InventoryOrphanNodes.Set(float64(resolverDiagnosticCount(diagnostics, resolver.DiagnosticSourceInventory, "PowerPlanningOrphan")))
	metrics.InventoryCommunicationPathUnmodeledNodes.Set(float64(resolverDiagnosticCount(diagnostics, resolver.DiagnosticSourceInventory, "CommunicationPathUnmodeled")))
}

func recordCapabilityMatchMetric(match capability.MatchResult, err error) {
	if err != nil {
		metrics.CapabilityMatchTotal.WithLabelValues("Failed", "None", "false").Inc()
		return
	}
	tier := string(match.Tier)
	if tier == "" {
		tier = "Unknown"
	}
	metrics.CapabilityMatchTotal.WithLabelValues("Matched", tier, strconv.FormatBool(match.Unidentified)).Inc()
}

func resolverDiagnosticCount(diagnostics []resolver.Diagnostic, source, reason string) int {
	var count int
	for _, diagnostic := range diagnostics {
		if diagnostic.Source == source && diagnostic.Reason == reason {
			count++
		}
	}
	return count
}

// snapshotAgeLevels reads the cluster's IN-16 escalation thresholds.
//
// An unconfigured cluster gets the defaults rather than silence: the rule exists
// so a stale snapshot cannot pass unnoticed, and an install that never wrote the
// field is exactly the install that would not notice.
func snapshotAgeLevels(cluster *powerv1alpha1.PowerManagementCluster) []resolver.SnapshotAgeLevel {
	if cluster == nil || len(cluster.Spec.Inventory.SnapshotAgeLevels) == 0 {
		return resolver.DefaultSnapshotAgeLevels()
	}
	levels := make([]resolver.SnapshotAgeLevel, 0, len(cluster.Spec.Inventory.SnapshotAgeLevels))
	for _, level := range cluster.Spec.Inventory.SnapshotAgeLevels {
		levels = append(levels, resolver.SnapshotAgeLevel{
			Severity: string(level.Level),
			After:    level.After.Duration,
		})
	}
	return levels
}

// telemetryAliasesFromMatch hands the matched profile's alias map to the
// normalizer. Both sides use the same shape, so this is a copy, not a
// translation.
func telemetryAliasesFromMatch(match capability.MatchResult) map[string]string {
	return copyStringMap(match.TelemetryAliases)
}

// capabilityStatusFromMatch publishes what a device resolved to.
//
// Facts only, per EX-28: the profile identity, the tier that produced it, the quirks in force after
// firmware scoping, and the machine reason when the resolution is anything other than a clean
// product match. Whether that is acceptable is the reader's call to make from the tier -- a
// driver-family match is normal for a device the catalog has never seen and alarming for one it
// should know.
//
// A failed match still produces a status rather than nothing. The device keeps polling without
// alias resolution, so the failure is real but partial, and silence would present it as success.
func capabilityStatusFromMatch(match capability.MatchResult, diagnostics []capability.Diagnostic, err error) *powerv1alpha1.UPSDeviceCapabilityStatus {
	if err != nil {
		return &powerv1alpha1.UPSDeviceCapabilityStatus{
			Reason:  "CapabilityMatchFailed",
			Message: "capability profile resolution failed, so telemetry polls without alias resolution: " + err.Error(),
		}
	}

	status := &powerv1alpha1.UPSDeviceCapabilityStatus{
		ProfileID:      match.ProfileID,
		ProfileVersion: match.ProfileVersion,
		ProfileSource:  string(match.ProfileSource),
		ProfileHash:    match.ProfileHash,
		Tier:           string(match.Tier),
		Unidentified:   match.Unidentified,
		Quirks:         append([]string(nil), match.Quirks...),
		// Published even though nothing consumes it yet. F-27 is the missing verification
		// lifecycle, and the first thing that lifecycle needs is for a device to be able to say
		// which behaviors its profile currently claims -- which until now it could not.
		ActuationBehaviors: append([]string(nil), match.ActuationBehaviors...),
	}

	// The matcher already decided why a match is imperfect and said so in a diagnostic. Carrying the
	// first warning through rather than re-deriving it here keeps one explanation of a fallback
	// instead of two that can disagree.
	for _, diagnostic := range diagnostics {
		if diagnostic.Severity == capability.DiagnosticWarning && (diagnostic.Subject == "" || diagnostic.Subject == match.DeviceID) {
			status.Reason = diagnostic.Reason
			status.Message = diagnostic.Message
			break
		}
	}
	return status
}
