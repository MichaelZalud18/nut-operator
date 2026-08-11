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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/capability"
	"github.com/MichaelZalud18/nut-operator/internal/inventory"
)

func inventoryEntityFromUPSDevice(obj *powerv1alpha1.UPSDevice) inventory.Entity {
	return inventory.Entity{
		ID:                  obj.Name,
		Kind:                inventory.EntityKindUPSDevice,
		PowerDomains:        append([]string(nil), obj.Spec.PowerDomains...),
		Model:               obj.Spec.Identity.Model,
		Firmware:            obj.Spec.Identity.Firmware,
		DriverFamily:        obj.Spec.Driver,
		PowerPlanningExempt: false,
	}
}

func inventoryEntityFromPowerInfrastructure(obj *powerv1alpha1.PowerInfrastructure) inventory.Entity {
	return inventory.Entity{
		ID:   obj.Name,
		Kind: inventory.EntityKindPowerInfrastructure,
	}
}

func inventoryEntityFromPowerInventoryNode(obj *powerv1alpha1.PowerInventoryNode) inventory.Entity {
	return inventory.Entity{
		ID:                       obj.Spec.NodeName,
		Kind:                     inventory.EntityKindNode,
		PowerPlanningExempt:      boolPointerValue(obj.Spec.PowerPlanningExempt),
		CommunicationPathExempt:  boolPointerValue(obj.Spec.CommunicationPathExempt),
		ShutdownTier:             powerInventoryNodeShutdownTier(obj),
		LastDitchRole:            obj.Spec.Roles.LastDitchRole,
		ControlPlane:             boolPointerValue(obj.Spec.Roles.ControlPlane),
		ControlPlaneQuorumMember: boolPointerValue(obj.Spec.Roles.ControlPlaneQuorumMember),
	}
}

func powerInventoryNodeShutdownTier(obj *powerv1alpha1.PowerInventoryNode) *int32 {
	if obj == nil {
		return nil
	}
	if obj.Spec.Roles.ShutdownTier != nil {
		tier := *obj.Spec.Roles.ShutdownTier
		return &tier
	}
	if obj.Spec.Roles.LastDitchRole != "" {
		tier := int32(1)
		return &tier
	}
	return nil
}

func inventoryEdgeFromPowerInventoryEdge(obj *powerv1alpha1.PowerInventoryEdge) inventory.Edge {
	return inventory.Edge{
		From:     obj.Spec.From.Name,
		To:       obj.Spec.To.Name,
		Relation: inventoryRelationFromAPI(obj.Spec.Relation),
		Input:    obj.Spec.Input,
		SourceID: fmt.Sprintf("%s/PowerInventoryEdge/%s", powerv1alpha1.GroupVersion.String(), obj.Name),
	}
}

func inventoryRelationFromAPI(relation powerv1alpha1.PowerInventoryEdgeRelation) inventory.EdgeRelation {
	switch relation {
	case powerv1alpha1.PowerInventoryEdgeFeeds:
		return inventory.EdgeRelationFeeds
	case powerv1alpha1.PowerInventoryEdgeCarries:
		return inventory.EdgeRelationCarries
	default:
		return inventory.EdgeRelation(relation)
	}
}

func capabilityProfileFromUPSCapabilityProfile(obj *powerv1alpha1.UPSCapabilityProfile) capability.Profile {
	selector := obj.Spec.Selector
	return capability.Profile{
		ID:      obj.Name,
		Version: obj.Spec.Version,
		Source:  capability.ProfileSourceCRD,
		Selector: capability.ProfileSelector{
			Model:        selector.Model,
			Firmware:     selector.Firmware,
			ModelGlob:    selector.ModelGlob,
			DriverFamily: selector.DriverFamily,
			Universal:    boolPointerValue(selector.Universal),
		},
		TelemetryVariables: append([]string(nil), obj.Spec.Telemetry.Variables...),
		TelemetryAliases:   copyStringMap(obj.Spec.Telemetry.Aliases),
		ActuationBehaviors: append([]string(nil), obj.Spec.Actuation.Behaviors...),
		Quirks:             capabilityQuirksFromCRD(obj.Spec.Quirks),
		RuntimeEstimate:    capability.RuntimeEstimate(obj.Spec.Telemetry.RuntimeEstimate),
	}
}

// pduCapabilityProfileFromCRD converts the PDU kind into the matcher's shape.
func pduCapabilityProfileFromCRD(obj *powerv1alpha1.PDUCapabilityProfile) capability.PDUProfile {
	selector := obj.Spec.Selector
	return capability.PDUProfile{
		ID:      obj.Name,
		Version: obj.Spec.Version,
		Source:  capability.ProfileSourceCRD,
		Selector: capability.ProfileSelector{
			Model:        selector.Model,
			Firmware:     selector.Firmware,
			ModelGlob:    selector.ModelGlob,
			DriverFamily: selector.DriverFamily,
			Universal:    boolPointerValue(selector.Universal),
		},
		Outlets: capability.PDUOutlets{
			Count:      int(obj.Spec.Outlets.Count),
			Switchable: append([]string(nil), obj.Spec.Outlets.Switchable...),
		},
		TelemetryVariables: append([]string(nil), obj.Spec.Telemetry.Variables...),
		TelemetryAliases:   copyStringMap(obj.Spec.Telemetry.Aliases),
		Quirks:             capabilityQuirksFromCRD(obj.Spec.Quirks),
	}
}

// capabilityQuirksFromCRD converts declared quirks, carrying firmware scope
// through so the matcher can decide which ones are in force (F-26).
func capabilityQuirksFromCRD(quirks []powerv1alpha1.CapabilityQuirk) []capability.Quirk {
	if len(quirks) == 0 {
		return nil
	}
	converted := make([]capability.Quirk, 0, len(quirks))
	for _, quirk := range quirks {
		next := capability.Quirk{Name: quirk.Name}
		if quirk.Firmware != nil {
			next.Firmware = &capability.QuirkFirmware{
				Matches: append([]string(nil), quirk.Firmware.Matches...),
				Below:   quirk.Firmware.Below,
			}
		}
		converted = append(converted, next)
	}
	return converted
}

// copyStringMap defensively copies an alias map so a cached API object can
// never be mutated through the profile the resolver hands downstream.
func copyStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	copied := make(map[string]string, len(values))
	for key, value := range values {
		copied[key] = value
	}
	return copied
}

func boolPointerValue(value *bool) bool {
	return value != nil && *value
}

func hashJSON(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("controller value could not be encoded for hashing: %v", err))
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
