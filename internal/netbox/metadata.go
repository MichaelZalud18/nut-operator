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

package netbox

import (
	"encoding/json"
	"fmt"
	"strings"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

// DeviceMetadata is the JSON payload read from the NetBox custom field named by
// MappingOptions.OperatorCustomField. It carries only fields consumed by
// nut-operator planner rules or by the existing UPSDevice operand renderer.
type DeviceMetadata struct {
	Kind                    string           `json:"kind,omitempty"`
	Name                    string           `json:"name,omitempty"`
	NodeName                string           `json:"nodeName,omitempty"`
	InfrastructureClass     string           `json:"infrastructureClass,omitempty"`
	PowerDomain             string           `json:"powerDomain,omitempty"`
	PowerDomains            []string         `json:"powerDomains,omitempty"`
	PowerPlanningExempt     *bool            `json:"powerPlanningExempt,omitempty"`
	CommunicationPathExempt *bool            `json:"communicationPathExempt,omitempty"`
	Roles                   NodeRoleMetadata `json:"roles,omitempty"`
	NUT                     *NUTMetadata     `json:"nut,omitempty"`
}

// NodeRoleMetadata carries planner-relevant Kubernetes node roles.
type NodeRoleMetadata struct {
	ControlPlane             *bool  `json:"controlPlane,omitempty"`
	ControlPlaneQuorumMember *bool  `json:"controlPlaneQuorumMember,omitempty"`
	ShutdownTier             *int32 `json:"shutdownTier,omitempty"`
	LastDitchRole            string `json:"lastDitchRole,omitempty"`
}

// NUTMetadata carries fields required to render a UPSDevice from NetBox.
type NUTMetadata struct {
	Driver              string                                 `json:"driver,omitempty"`
	EndpointHost        string                                 `json:"endpointHost,omitempty"`
	EndpointPort        *int32                                 `json:"endpointPort,omitempty"`
	Model               string                                 `json:"model,omitempty"`
	Firmware            string                                 `json:"firmware,omitempty"`
	DriverOptions       map[string]string                      `json:"driverOptions,omitempty"`
	CredentialSecretRef *powerv1alpha1.NamespacedNameReference `json:"credentialSecretRef,omitempty"`
	UpstreamNUT         *powerv1alpha1.UPSUpstreamNUTSpec      `json:"upstreamNUT,omitempty"`
}

func metadataForDevice(device Device, field string) (DeviceMetadata, bool, error) {
	if field == "" {
		field = DefaultOperatorCustomField
	}
	raw, ok := device.CustomFields[field]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return DeviceMetadata{}, false, nil
	}

	var meta DeviceMetadata
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return DeviceMetadata{}, false, fmt.Errorf("decode custom field %q on NetBox device %d: %w", field, device.ID, err)
		}
		text = strings.TrimSpace(text)
		if text == "" {
			return DeviceMetadata{}, false, nil
		}
		if strings.HasPrefix(text, "{") {
			if err := json.Unmarshal([]byte(text), &meta); err != nil {
				return DeviceMetadata{}, false, fmt.Errorf("decode JSON custom field %q on NetBox device %d: %w", field, device.ID, err)
			}
		} else {
			meta.Kind = text
		}
	} else if err := json.Unmarshal(raw, &meta); err != nil {
		return DeviceMetadata{}, false, fmt.Errorf("decode custom field %q on NetBox device %d: %w", field, device.ID, err)
	}

	meta.Kind = normalizeKind(meta.Kind)
	meta.InfrastructureClass = normalizeInfrastructureClass(meta.InfrastructureClass)
	meta.PowerDomains = normalizePowerDomains(meta.PowerDomain, meta.PowerDomains)
	return meta, true, nil
}

func normalizeKind(kind string) string {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "", "none":
		return ""
	case "ups", "upsdevice", "ups-device":
		return "UPSDevice"
	case "node", "kubernetesnode", "kubernetes-node":
		return "Node"
	case "powerinfrastructure", "power-infrastructure", "infrastructure", "infra":
		return "PowerInfrastructure"
	default:
		return kind
	}
}

func normalizePowerDomains(single string, many []string) []string {
	seen := map[string]struct{}{}
	var domains []string
	for _, value := range append([]string{single}, many...) {
		for _, part := range strings.Split(value, ",") {
			domain := strings.TrimSpace(part)
			if domain == "" {
				continue
			}
			if _, exists := seen[domain]; exists {
				continue
			}
			seen[domain] = struct{}{}
			domains = append(domains, domain)
		}
	}
	return domains
}

func normalizeInfrastructureClass(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "other":
		return ""
	case "pdu":
		return "PDU"
	case "switch":
		return "Switch"
	case "router":
		return "Router"
	case "transferswitch", "transfer-switch":
		return "TransferSwitch"
	case "powerpanel", "power-panel":
		return "PowerPanel"
	case "networkdevice", "network-device", "network":
		return "NetworkDevice"
	default:
		return value
	}
}
