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
		LastDitchRole:            obj.Spec.Roles.LastDitchRole,
		ControlPlane:             boolPointerValue(obj.Spec.Roles.ControlPlane),
		ControlPlaneQuorumMember: boolPointerValue(obj.Spec.Roles.ControlPlaneQuorumMember),
	}
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
		ActuationBehaviors: append([]string(nil), obj.Spec.Actuation.Behaviors...),
		Quirks:             append([]string(nil), obj.Spec.Quirks...),
	}
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
