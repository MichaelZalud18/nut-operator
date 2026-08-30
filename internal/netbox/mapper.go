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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/inventory"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
)

const (
	DiagnosticWarning = "Warning"
	DiagnosticError   = "Error"
)

// MappingOptions controls NetBox-to-nut-operator normalization.
type MappingOptions struct {
	OperatorCustomField string
}

// Manifest is the operator-native inventory rendered from one NetBox snapshot.
type Manifest struct {
	SourceID            string
	ObservedAt          string
	Snapshot            inventory.Snapshot
	Diagnostics         []Diagnostic
	UPSDevices          []powerv1alpha1.UPSDevice
	PowerInfrastructure []powerv1alpha1.PowerInfrastructure
	PowerInventoryNodes []powerv1alpha1.PowerInventoryNode
	PowerInventoryEdges []powerv1alpha1.PowerInventoryEdge
}

// Diagnostic is a mapper-level warning or error.
type Diagnostic struct {
	Severity string `json:"severity"`
	Reason   string `json:"reason"`
	Subject  string `json:"subject,omitempty"`
	Message  string `json:"message"`
}

// BuildManifest maps a NetBox DCIM snapshot into ordinary nut-operator CRs and
// the provider-neutral inventory snapshot those CRs represent.
func BuildManifest(source Source, options MappingOptions) (Manifest, error) {
	if options.OperatorCustomField == "" {
		options.OperatorCustomField = DefaultOperatorCustomField
	}
	observedAt := source.ObservedAt.UTC()
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	manifest := Manifest{
		SourceID:   "netbox",
		ObservedAt: observedAt.Format(time.RFC3339),
	}
	manifest.Snapshot = inventory.Snapshot{
		SourceID:   "netbox",
		Version:    "v1",
		ObservedAt: manifest.ObservedAt,
	}

	devices := append([]Device(nil), source.Devices...)
	sort.SliceStable(devices, func(i, j int) bool { return devices[i].ID < devices[j].ID })

	entities := map[int]entityReference{}
	entityNames := map[string]int{}
	for _, device := range devices {
		meta, present, err := metadataForDevice(device, options.OperatorCustomField)
		if err != nil {
			return Manifest{}, err
		}
		kind, class, ok := classifyDevice(device, meta, present)
		if !ok {
			manifest.Diagnostics = append(manifest.Diagnostics, Diagnostic{
				Severity: DiagnosticWarning,
				Reason:   "DeviceSkipped",
				Subject:  fmt.Sprintf("dcim.Device/%d", device.ID),
				Message:  fmt.Sprintf("NetBox device %q has no %q custom field and no infrastructure role, so it was not imported", device.display(), options.OperatorCustomField),
			})
			continue
		}

		ref, err := manifest.addDevice(device, meta, kind, class)
		if err != nil {
			return Manifest{}, err
		}
		if previous, exists := entityNames[ref.Name]; exists {
			return Manifest{}, fmt.Errorf("NetBox devices %d and %d both map to inventory entity %q", previous, device.ID, ref.Name)
		}
		entityNames[ref.Name] = device.ID
		entities[device.ID] = ref
	}

	manifest.addPowerFeedEdges(source.PowerPorts, entities)
	manifest.addCommunicationEdges(source.Interfaces, entities)
	manifest.sort()
	return manifest, nil
}

type entityReference struct {
	Kind powerv1alpha1.PowerInventoryEntityKind
	Name string
}

func classifyDevice(device Device, meta DeviceMetadata, hasMetadata bool) (string, string, bool) {
	if meta.Kind != "" {
		return meta.Kind, meta.InfrastructureClass, true
	}
	class := inferInfrastructureClass(device)
	if class != "" {
		return "PowerInfrastructure", class, true
	}
	return "", "", false
}

func (m *Manifest) addDevice(device Device, meta DeviceMetadata, kind, class string) (entityReference, error) {
	switch kind {
	case "UPSDevice":
		return m.addUPSDevice(device, meta)
	case "Node":
		return m.addInventoryNode(device, meta)
	case "PowerInfrastructure":
		return m.addInfrastructure(device, meta, class)
	default:
		return entityReference{}, fmt.Errorf("NetBox device %d declares unsupported nut-operator kind %q", device.ID, kind)
	}
}

func (m *Manifest) addUPSDevice(device Device, meta DeviceMetadata) (entityReference, error) {
	if meta.NUT == nil {
		return entityReference{}, fmt.Errorf("NetBox device %d declares UPSDevice but omits nut metadata", device.ID)
	}
	name, err := objectName(meta.Name, device.Name, device.ID)
	if err != nil {
		return entityReference{}, err
	}
	if len(meta.PowerDomains) == 0 {
		return entityReference{}, fmt.Errorf("NetBox UPS device %q requires powerDomains in nut-operator metadata", device.display())
	}
	if meta.NUT.UpstreamNUT == nil && meta.NUT.Driver == "" {
		return entityReference{}, fmt.Errorf("NetBox UPS device %q requires nut.driver unless nut.upstreamNUT is set", device.display())
	}
	if meta.NUT.UpstreamNUT == nil && meta.NUT.Driver != "dummy-ups" && meta.NUT.EndpointHost == "" {
		return entityReference{}, fmt.Errorf("NetBox UPS device %q requires nut.endpointHost for network NUT drivers", device.display())
	}

	spec := powerv1alpha1.UPSDeviceSpec{
		DisplayName: device.display(),
		Identity: powerv1alpha1.UPSDeviceIdentitySpec{
			Model:    firstNonEmpty(meta.NUT.Model, deviceModel(device)),
			Firmware: meta.NUT.Firmware,
		},
		Driver:              meta.NUT.Driver,
		PowerDomains:        append([]string(nil), meta.PowerDomains...),
		DriverOptions:       copyStringMap(meta.NUT.DriverOptions),
		CredentialSecretRef: meta.NUT.CredentialSecretRef,
		UpstreamNUT:         meta.NUT.UpstreamNUT,
	}
	if meta.NUT.EndpointHost != "" {
		spec.Endpoint = &powerv1alpha1.UPSEndpointSpec{
			Host: meta.NUT.EndpointHost,
			Port: meta.NUT.EndpointPort,
		}
	}
	deviceObj := powerv1alpha1.UPSDevice{
		TypeMeta:   objectTypeMeta("UPSDevice"),
		ObjectMeta: netBoxObjectMeta(name, device),
		Spec:       spec,
	}
	m.UPSDevices = append(m.UPSDevices, deviceObj)
	m.Snapshot.Entities = append(m.Snapshot.Entities, inventory.Entity{
		ID:           name,
		Kind:         inventory.EntityKindUPSDevice,
		PowerDomains: append([]string(nil), meta.PowerDomains...),
		Model:        spec.Identity.Model,
		Firmware:     spec.Identity.Firmware,
		DriverFamily: spec.Driver,
	})
	return entityReference{Kind: powerv1alpha1.PowerInventoryEntityUPSDevice, Name: name}, nil
}

func (m *Manifest) addInventoryNode(device Device, meta DeviceMetadata) (entityReference, error) {
	nodeName := firstNonEmpty(meta.NodeName, meta.Name, device.Name)
	if strings.TrimSpace(nodeName) == "" {
		return entityReference{}, fmt.Errorf("NetBox node device %q requires nodeName or name in nut-operator metadata", device.display())
	}
	metadataName, err := objectName(meta.Name, nodeName, device.ID)
	if err != nil {
		return entityReference{}, err
	}
	node := powerv1alpha1.PowerInventoryNode{
		TypeMeta:   objectTypeMeta("PowerInventoryNode"),
		ObjectMeta: netBoxObjectMeta(metadataName, device),
		Spec: powerv1alpha1.PowerInventoryNodeSpec{
			NodeName:                nodeName,
			PowerPlanningExempt:     meta.PowerPlanningExempt,
			CommunicationPathExempt: meta.CommunicationPathExempt,
			Roles: powerv1alpha1.PowerInventoryNodeRoles{
				ControlPlane:             meta.Roles.ControlPlane,
				ControlPlaneQuorumMember: meta.Roles.ControlPlaneQuorumMember,
				ShutdownTier:             meta.Roles.ShutdownTier,
				LastDitchRole:            meta.Roles.LastDitchRole,
			},
		},
	}
	m.PowerInventoryNodes = append(m.PowerInventoryNodes, node)
	m.Snapshot.Entities = append(m.Snapshot.Entities, inventory.Entity{
		ID:                       nodeName,
		Kind:                     inventory.EntityKindNode,
		PowerPlanningExempt:      boolPointerValue(meta.PowerPlanningExempt),
		CommunicationPathExempt:  boolPointerValue(meta.CommunicationPathExempt),
		ShutdownTier:             meta.Roles.ShutdownTier,
		LastDitchRole:            meta.Roles.LastDitchRole,
		ControlPlane:             boolPointerValue(meta.Roles.ControlPlane),
		ControlPlaneQuorumMember: boolPointerValue(meta.Roles.ControlPlaneQuorumMember),
	})
	return entityReference{Kind: powerv1alpha1.PowerInventoryEntityNode, Name: nodeName}, nil
}

func (m *Manifest) addInfrastructure(device Device, meta DeviceMetadata, class string) (entityReference, error) {
	name, err := objectName(meta.Name, device.Name, device.ID)
	if err != nil {
		return entityReference{}, err
	}
	if class == "" {
		class = meta.InfrastructureClass
	}
	spec := powerv1alpha1.PowerInfrastructureSpec{
		DisplayName: device.display(),
		Description: device.Description,
		Class:       apiInfrastructureClass(class),
	}
	infra := powerv1alpha1.PowerInfrastructure{
		TypeMeta:   objectTypeMeta("PowerInfrastructure"),
		ObjectMeta: netBoxObjectMeta(name, device),
		Spec:       spec,
	}
	m.PowerInfrastructure = append(m.PowerInfrastructure, infra)
	m.Snapshot.Entities = append(m.Snapshot.Entities, inventory.Entity{
		ID:   name,
		Kind: inventory.EntityKindPowerInfrastructure,
	})
	return entityReference{Kind: powerv1alpha1.PowerInventoryEntityPowerInfrastructure, Name: name}, nil
}

func (m *Manifest) addPowerFeedEdges(ports []PowerPort, entities map[int]entityReference) {
	ports = append([]PowerPort(nil), ports...)
	sort.SliceStable(ports, func(i, j int) bool { return ports[i].ID < ports[j].ID })
	seen := map[string]struct{}{}
	for _, port := range ports {
		target, ok := entities[port.Device.ID]
		if !ok {
			continue
		}
		for _, endpoint := range componentEndpoints(port.ConnectedEndpoints, port.ConnectedEndpoint) {
			if endpoint.Device == nil {
				m.addUnmappedPowerEndpoint(port, endpoint)
				continue
			}
			source, ok := entities[endpoint.Device.ID]
			if !ok {
				m.addUnmappedPowerEndpoint(port, endpoint)
				continue
			}
			if source == target {
				continue
			}
			m.addEdge(seen, source, target, powerv1alpha1.PowerInventoryEdgeFeeds, firstNonEmpty(port.Name, port.Label), fmt.Sprintf("netbox/dcim.PowerPort/%d", port.ID))
		}
	}
}

func (m *Manifest) addCommunicationEdges(interfaces []Interface, entities map[int]entityReference) {
	interfaces = append([]Interface(nil), interfaces...)
	sort.SliceStable(interfaces, func(i, j int) bool { return interfaces[i].ID < interfaces[j].ID })
	seen := map[string]struct{}{}
	for _, iface := range interfaces {
		local, ok := entities[iface.Device.ID]
		if !ok {
			continue
		}
		for _, endpoint := range componentEndpoints(iface.ConnectedEndpoints, iface.ConnectedEndpoint) {
			if endpoint.Device == nil {
				continue
			}
			peer, ok := entities[endpoint.Device.ID]
			if !ok || peer == local {
				continue
			}
			source, target, ok := communicationDirection(local, peer)
			if !ok {
				continue
			}
			m.addEdge(seen, source, target, powerv1alpha1.PowerInventoryEdgeCarries, "", fmt.Sprintf("netbox/dcim.Interface/%d", iface.ID))
		}
	}
}

func (m *Manifest) addEdge(seen map[string]struct{}, from, to entityReference, relation powerv1alpha1.PowerInventoryEdgeRelation, input, sourceID string) {
	key := fmt.Sprintf("%s|%s|%s|%s", from.Name, to.Name, relation, input)
	if _, exists := seen[key]; exists {
		return
	}
	seen[key] = struct{}{}

	name := edgeName(from.Name, to.Name, string(relation), input)
	edge := powerv1alpha1.PowerInventoryEdge{
		TypeMeta: objectTypeMeta("PowerInventoryEdge"),
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
			Labels: map[string]string{
				"app.kubernetes.io/name":       "nut-operator",
				"power.zalud.io/source-system": "netbox",
			},
			Annotations: map[string]string{
				"netbox.power.zalud.io/source-id": sourceID,
			},
		},
		Spec: powerv1alpha1.PowerInventoryEdgeSpec{
			From:     apiEntityReference(from),
			To:       apiEntityReference(to),
			Relation: relation,
			Input:    input,
		},
	}
	m.PowerInventoryEdges = append(m.PowerInventoryEdges, edge)
	m.Snapshot.Edges = append(m.Snapshot.Edges, inventory.Edge{
		From:     from.Name,
		To:       to.Name,
		Relation: inventoryRelation(relation),
		Input:    input,
		SourceID: sourceID,
	})
}

func (m *Manifest) addUnmappedPowerEndpoint(port PowerPort, endpoint Endpoint) {
	m.Diagnostics = append(m.Diagnostics, Diagnostic{
		Severity: DiagnosticWarning,
		Reason:   "PowerEndpointUnmapped",
		Subject:  fmt.Sprintf("dcim.PowerPort/%d", port.ID),
		Message:  fmt.Sprintf("power port %q is connected to %q, but the endpoint's device is not in the imported NetBox device set", port.Name, endpoint.display()),
	})
}

func communicationDirection(local, peer entityReference) (entityReference, entityReference, bool) {
	if local.Kind == powerv1alpha1.PowerInventoryEntityPowerInfrastructure && peer.Kind != powerv1alpha1.PowerInventoryEntityPowerInfrastructure {
		return local, peer, true
	}
	if peer.Kind == powerv1alpha1.PowerInventoryEntityPowerInfrastructure && local.Kind != powerv1alpha1.PowerInventoryEntityPowerInfrastructure {
		return peer, local, true
	}
	return entityReference{}, entityReference{}, false
}

func (m *Manifest) sort() {
	sort.SliceStable(m.UPSDevices, func(i, j int) bool { return m.UPSDevices[i].Name < m.UPSDevices[j].Name })
	sort.SliceStable(m.PowerInfrastructure, func(i, j int) bool { return m.PowerInfrastructure[i].Name < m.PowerInfrastructure[j].Name })
	sort.SliceStable(m.PowerInventoryNodes, func(i, j int) bool { return m.PowerInventoryNodes[i].Name < m.PowerInventoryNodes[j].Name })
	sort.SliceStable(m.PowerInventoryEdges, func(i, j int) bool { return m.PowerInventoryEdges[i].Name < m.PowerInventoryEdges[j].Name })
	sort.SliceStable(m.Snapshot.Entities, func(i, j int) bool { return m.Snapshot.Entities[i].ID < m.Snapshot.Entities[j].ID })
	sort.SliceStable(m.Snapshot.Edges, func(i, j int) bool {
		left := fmt.Sprintf("%s|%s|%s|%s", m.Snapshot.Edges[i].From, m.Snapshot.Edges[i].To, m.Snapshot.Edges[i].Relation, m.Snapshot.Edges[i].Input)
		right := fmt.Sprintf("%s|%s|%s|%s", m.Snapshot.Edges[j].From, m.Snapshot.Edges[j].To, m.Snapshot.Edges[j].Relation, m.Snapshot.Edges[j].Input)
		return left < right
	})
}

func netBoxObjectMeta(name string, device Device) metav1.ObjectMeta {
	return metav1.ObjectMeta{
		Name: name,
		Labels: map[string]string{
			"app.kubernetes.io/name":       "nut-operator",
			"power.zalud.io/source-system": "netbox",
		},
		Annotations: map[string]string{
			"netbox.power.zalud.io/device-id": strconv.Itoa(device.ID),
		},
	}
}

func objectTypeMeta(kind string) metav1.TypeMeta {
	return metav1.TypeMeta{
		APIVersion: powerv1alpha1.GroupVersion.String(),
		Kind:       kind,
	}
}

func apiEntityReference(ref entityReference) powerv1alpha1.PowerInventoryEntityReference {
	return powerv1alpha1.PowerInventoryEntityReference{
		Kind: ref.Kind,
		Name: ref.Name,
	}
}

func objectName(explicit, fallback string, id int) (string, error) {
	if explicit != "" {
		if errs := validation.IsDNS1123Subdomain(explicit); len(errs) > 0 {
			return "", fmt.Errorf("inventory object name %q from NetBox device %d is not a valid Kubernetes name: %s", explicit, id, strings.Join(errs, "; "))
		}
		return explicit, nil
	}
	name := dns1123Name(fallback)
	if name == "" {
		name = fmt.Sprintf("netbox-device-%d", id)
	}
	return name, nil
}

var invalidNameCharacter = regexp.MustCompile(`[^a-z0-9.-]+`)

func dns1123Name(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = invalidNameCharacter.ReplaceAllString(value, "-")
	value = strings.Trim(value, "-.")
	if len(value) <= 253 {
		return value
	}
	sum := shortHash(value)
	return strings.Trim(value[:244], "-.") + "-" + sum
}

func edgeName(from, to, relation, input string) string {
	base := dns1123Name(strings.Join([]string{from, relation, to, input}, "-"))
	if base == "" {
		base = "netbox-inventory-edge"
	}
	sum := shortHash(strings.Join([]string{from, to, relation, input}, "|"))
	if len(base) > 244 {
		base = strings.Trim(base[:244], "-.")
	}
	return base + "-" + sum
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])[:8]
}

func inferInfrastructureClass(device Device) string {
	if device.Role == nil {
		return ""
	}
	text := strings.ToLower(device.Role.text())
	switch {
	case strings.Contains(text, "pdu"):
		return "PDU"
	case strings.Contains(text, "switch"):
		return "Switch"
	case strings.Contains(text, "router"):
		return "Router"
	case strings.Contains(text, "power-panel"), strings.Contains(text, "power panel"):
		return "PowerPanel"
	case strings.Contains(text, "network"):
		return "NetworkDevice"
	default:
		return ""
	}
}

func apiInfrastructureClass(class string) powerv1alpha1.PowerInfrastructureClass {
	switch normalizeInfrastructureClass(class) {
	case "PDU":
		return powerv1alpha1.PowerInfrastructureClassPDU
	case "Switch":
		return powerv1alpha1.PowerInfrastructureClassSwitch
	case "Router":
		return powerv1alpha1.PowerInfrastructureClassRouter
	case "TransferSwitch":
		return powerv1alpha1.PowerInfrastructureClassTransferSwitch
	case "PowerPanel":
		return powerv1alpha1.PowerInfrastructureClassPowerPanel
	case "NetworkDevice":
		return powerv1alpha1.PowerInfrastructureClassNetworkDevice
	default:
		if strings.TrimSpace(class) == "" {
			return ""
		}
		return powerv1alpha1.PowerInfrastructureClassOther
	}
}

func inventoryRelation(relation powerv1alpha1.PowerInventoryEdgeRelation) inventory.EdgeRelation {
	switch relation {
	case powerv1alpha1.PowerInventoryEdgeFeeds:
		return inventory.EdgeRelationFeeds
	case powerv1alpha1.PowerInventoryEdgeCarries:
		return inventory.EdgeRelationCarries
	default:
		return inventory.EdgeRelation(relation)
	}
}

func deviceModel(device Device) string {
	if device.DeviceType == nil {
		return ""
	}
	if device.DeviceType.Manufacturer != nil && device.DeviceType.Manufacturer.text() != "" {
		return strings.TrimSpace(device.DeviceType.Manufacturer.text() + " " + device.DeviceType.Model)
	}
	return device.DeviceType.Model
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

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

func (e Endpoint) display() string {
	for _, value := range []string{e.Display, e.Name, e.URL} {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	if e.ID > 0 {
		return fmt.Sprintf("endpoint %d", e.ID)
	}
	return "endpoint"
}
