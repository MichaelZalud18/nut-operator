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
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
	shutdownflowadapter "github.com/MichaelZalud18/nut-operator/internal/shutdownflow"
)

type validationResult struct {
	accepted bool
	reason   string
	message  string
}

var semanticVersionPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

func accepted(message string) validationResult {
	return validationResult{accepted: true, reason: "Accepted", message: message}
}

func rejected(reason, format string, args ...any) validationResult {
	return validationResult{accepted: false, reason: reason, message: fmt.Sprintf(format, args...)}
}

func validatePowerManagementCluster(obj *powerv1alpha1.PowerManagementCluster) validationResult {
	if result := validatePowerShutdownTiers(obj.Spec.ShutdownTiers); !result.accepted {
		return result
	}
	switch obj.Spec.Storage.Mode {
	case "", powerv1alpha1.PowerStorageCNPG:
		if obj.Spec.Storage.CNPG == nil {
			return rejected("StorageNotConfigured", "CNPG storage mode requires spec.storage.cnpg.clusterRef")
		}
	case powerv1alpha1.PowerStorageExternalPostgres:
		if obj.Spec.Storage.ExternalPostgres == nil {
			return rejected("StorageNotConfigured", "ExternalPostgres storage mode requires spec.storage.externalPostgres.dsnSecretKeyRef")
		}
	case powerv1alpha1.PowerStorageDisabled:
		return accepted("storage is disabled; suitable only for development or tests")
	default:
		return rejected("UnsupportedStorageMode", "unsupported storage mode %q", obj.Spec.Storage.Mode)
	}

	return accepted("power management cluster contract accepted")
}

func validatePowerShutdownTiers(policy powerv1alpha1.PowerShutdownTierPolicySpec) validationResult {
	if policy.DefaultTier != nil && *policy.DefaultTier < 2 {
		return rejected("ShutdownTierDefaultReserved", "spec.shutdownTiers.defaultTier must be 2 or greater; tiers 0 and 1 are reserved")
	}
	tiers := map[int32]struct{}{}
	for _, tier := range policy.Tiers {
		if tier.Tier < 0 {
			return rejected("ShutdownTierInvalid", "spec.shutdownTiers.tiers cannot contain negative tier %d", tier.Tier)
		}
		if _, exists := tiers[tier.Tier]; exists {
			return rejected("DuplicateShutdownTier", "spec.shutdownTiers.tiers defines tier %d more than once", tier.Tier)
		}
		tiers[tier.Tier] = struct{}{}
	}
	rules := map[string]struct{}{}
	for _, rule := range policy.SelectorRules {
		if rule.Name == "" {
			return rejected("ShutdownTierRuleNameRequired", "spec.shutdownTiers.selectorRules requires every rule to have a name")
		}
		if _, exists := rules[rule.Name]; exists {
			return rejected("DuplicateShutdownTierRule", "spec.shutdownTiers.selectorRules contains duplicate rule %q", rule.Name)
		}
		rules[rule.Name] = struct{}{}
		if rule.Tier < 0 {
			return rejected("ShutdownTierInvalid", "spec.shutdownTiers.selectorRules[%q] assigns negative tier %d", rule.Name, rule.Tier)
		}
		if rule.Subject == powerv1alpha1.PowerShutdownTierSubjectNode && rule.Tier == 0 {
			return rejected("ShutdownTierZeroNode", "spec.shutdownTiers.selectorRules[%q] cannot assign tier 0 to nodes", rule.Name)
		}
	}
	return accepted("shutdown tier policy accepted")
}

func validateUPSDevice(obj *powerv1alpha1.UPSDevice) validationResult {
	if obj.Spec.UpstreamNUT != nil {
		return validateUpstreamNUTUPSDevice(obj)
	}
	if obj.Spec.Driver == "" {
		return rejected("DriverRequired", "spec.driver is required unless spec.upstreamNUT is set")
	}
	if isUnsupportedLocalUPSDriver(obj.Spec.Driver) {
		return rejected("LocalDriverUnsupported", "driver %q requires local USB or serial access; this operator currently supports network-reachable UPS devices only", obj.Spec.Driver)
	}
	if !isSupportedNetworkUPSDriver(obj.Spec.Driver) {
		return rejected("DriverUnsupported", "driver %q is not in the supported network driver allowlist", obj.Spec.Driver)
	}
	if obj.Spec.Driver != "dummy-ups" && obj.Spec.Endpoint == nil {
		return rejected("EndpointRequired", "spec.endpoint is required for network-reachable NUT drivers")
	}
	if obj.Spec.Endpoint != nil && obj.Spec.Endpoint.Host == "" {
		return rejected("EndpointHostRequired", "spec.endpoint.host is required")
	}
	if obj.Spec.Identity.Firmware != "" && obj.Spec.Identity.Model == "" {
		return rejected("IdentityFirmwareRequiresModel", "spec.identity.firmware requires spec.identity.model")
	}
	if obj.Spec.Simulation != nil {
		if obj.Spec.Driver != "dummy-ups" {
			return rejected("SimulationRequiresDummyUPS", "spec.simulation requires spec.driver dummy-ups")
		}
		if explicitPort := obj.Spec.DriverOptions["port"]; explicitPort != "" {
			return rejected("SimulationPortConflict", "spec.simulation cannot be combined with an explicit spec.driverOptions.port")
		}
		if obj.Spec.Simulation.SequenceConfigMapRef.Name == "" {
			return rejected("SimulationSequenceConfigMapRequired", "spec.simulation.sequenceConfigMapRef.name is required")
		}
	}

	return accepted("UPS device contract accepted")
}

func validateUpstreamNUTUPSDevice(obj *powerv1alpha1.UPSDevice) validationResult {
	upstream := obj.Spec.UpstreamNUT
	if obj.Spec.Driver != "" && obj.Spec.Driver != "dummy-ups" {
		return rejected("UpstreamNUTDriverConflict", "spec.driver must be empty or dummy-ups when spec.upstreamNUT is set")
	}
	if obj.Spec.Endpoint != nil {
		return rejected("UpstreamNUTEndpointConflict", "spec.endpoint cannot be set with spec.upstreamNUT")
	}
	if obj.Spec.CredentialSecretRef != nil {
		return rejected("UpstreamNUTCredentialConflict", "spec.credentialSecretRef cannot be set with spec.upstreamNUT; use spec.upstreamNUT.auth.secretKeyRef")
	}
	if obj.Spec.Simulation != nil {
		return rejected("UpstreamNUTSimulationConflict", "spec.simulation cannot be set with spec.upstreamNUT")
	}
	if upstream.Host == "" {
		return rejected("UpstreamNUTHostRequired", "spec.upstreamNUT.host is required")
	}
	if upstream.Port != nil && (*upstream.Port < 1 || *upstream.Port > 65535) {
		return rejected("UpstreamNUTPortInvalid", "spec.upstreamNUT.port must be between 1 and 65535")
	}
	if strings.ContainsAny(upstream.Host, "\r\n[]@:") {
		return rejected("UpstreamNUTHostInvalid", "spec.upstreamNUT.host contains unsupported NUT target characters")
	}
	if upstream.UPSName == "" {
		return rejected("UpstreamNUTUPSNameRequired", "spec.upstreamNUT.upsName is required")
	}
	if strings.ContainsAny(upstream.UPSName, "\r\n[]@") {
		return rejected("UpstreamNUTUPSNameInvalid", "spec.upstreamNUT.upsName contains unsupported NUT target characters")
	}
	for _, key := range []string{"port", "driver", "mode", "authconf", "repeater_disable_strict_start"} {
		if _, exists := obj.Spec.DriverOptions[key]; exists {
			return rejected("UpstreamNUTReservedDriverOption", "spec.driverOptions[%q] is rendered from spec.upstreamNUT and cannot be overridden", key)
		}
	}
	authMode := upstreamAuthMode(upstream)
	switch authMode {
	case powerv1alpha1.UPSUpstreamNUTAuthNone, powerv1alpha1.UPSUpstreamNUTAuthDefault:
		if upstream.Auth.SecretKeyRef != nil {
			return rejected("UpstreamNUTAuthSecretUnused", "spec.upstreamNUT.auth.secretKeyRef requires spec.upstreamNUT.auth.mode Secret")
		}
	case powerv1alpha1.UPSUpstreamNUTAuthSecret:
		if upstream.Auth.SecretKeyRef == nil {
			return rejected("UpstreamNUTAuthSecretRequired", "spec.upstreamNUT.auth.mode Secret requires spec.upstreamNUT.auth.secretKeyRef")
		}
	default:
		return rejected("UpstreamNUTAuthModeUnsupported", "unsupported spec.upstreamNUT.auth.mode %q", upstream.Auth.Mode)
	}
	if obj.Spec.Identity.Firmware != "" && obj.Spec.Identity.Model == "" {
		return rejected("IdentityFirmwareRequiresModel", "spec.identity.firmware requires spec.identity.model")
	}

	return accepted("upstream NUT device contract accepted")
}

func upstreamAuthMode(upstream *powerv1alpha1.UPSUpstreamNUTSpec) powerv1alpha1.UPSUpstreamNUTAuthMode {
	if upstream == nil || upstream.Auth.Mode == "" {
		return powerv1alpha1.UPSUpstreamNUTAuthNone
	}
	return upstream.Auth.Mode
}

func isSupportedNetworkUPSDriver(driver string) bool {
	switch driver {
	case "dummy-ups",
		"snmp-ups",
		"netxml-ups",
		"powerman-pdu",
		"apcupsd-ups":
		return true
	default:
		return false
	}
}

func isUnsupportedLocalUPSDriver(driver string) bool {
	switch driver {
	case "usbhid-ups",
		"nutdrv_qx",
		"blazer_usb",
		"richcomm_usb",
		"riello_usb",
		"tripplite_usb",
		"apcsmart",
		"bcmxcp",
		"bestups",
		"belkin",
		"genericups",
		"liebert",
		"mge-shut",
		"powercom",
		"safenet",
		"solis",
		"tripplite",
		"victronups":
		return true
	default:
		return false
	}
}

func validateNUTServer(obj *powerv1alpha1.NUTServer) validationResult {
	if len(obj.Spec.DeviceRefs) == 0 && obj.Spec.DeviceSelector == nil {
		return rejected("DeviceSelectionRequired", "spec.deviceRefs or spec.deviceSelector is required")
	}
	if obj.Spec.Auth.Mode == powerv1alpha1.NUTAuthExistingSecret && obj.Spec.Auth.ExistingSecretRef == nil {
		return rejected("AuthSecretRequired", "ExistingSecret auth mode requires spec.auth.existingSecretRef")
	}
	if obj.Spec.TLS.Mode == powerv1alpha1.NUTTLSRequired && obj.Spec.TLS.ServerCertificateRef == nil {
		return rejected("TLSServerCertificateRequired", "Required TLS mode requires spec.tls.serverCertificateRef")
	}

	return accepted("NUT server contract accepted")
}

func validateNodePowerAgent(obj *powerv1alpha1.NodePowerAgent) validationResult {
	if len(obj.Spec.NUTServerRefs) == 0 {
		return rejected("NUTServerRefsRequired", "spec.nutServerRefs requires at least one NUTServer reference")
	}
	// F-73: PullPolicy Always makes the agent unable to start at the moment it is needed.
	//
	// The hazard is specific to this operand. The images may live in a registry running inside the
	// cluster being shut down, so Always turns the one workload that must survive a power event into
	// one that cannot start without the thing the power event is taking away. IfNotPresent is the
	// default and the only safe value here; the image is pinned by tag or digest either way.
	for name, image := range map[string]powerv1alpha1.ImageReference{
		"upsmon":   obj.Spec.Images.Upsmon,
		"actuator": obj.Spec.Images.Actuator,
	} {
		if image.PullPolicy == corev1.PullAlways {
			return rejected("AgentImagePullPolicyAlways",
				"spec.images.%s.pullPolicy cannot be Always: the agent may need to start while the registry serving its image is itself being shut down", name)
		}
	}
	if obj.Spec.Mode == powerv1alpha1.NodePowerAgentModeActuate &&
		obj.Spec.Shutdown.ActuatorPolicy == powerv1alpha1.ActuatorPolicySystemdPoweroff {
		if obj.Spec.Shutdown.ApprovalAnnotation == "" {
			return rejected("ApprovalAnnotationRequired", "SystemdPoweroff actuation requires spec.shutdown.approvalAnnotation")
		}
		if obj.Annotations[obj.Spec.Shutdown.ApprovalAnnotation] != "true" {
			return rejected("ActuationNotApproved", "SystemdPoweroff actuation requires approval annotation %q=true", obj.Spec.Shutdown.ApprovalAnnotation)
		}
	}

	return accepted("node power agent contract accepted")
}

func validateShutdownFlow(obj *powerv1alpha1.ShutdownFlow) validationResult {
	_, diagnostics, err := planner.Compile(shutdownflowadapter.PlannerInputs(obj), planner.TelemetryInputs{})
	if err != nil {
		for _, diagnostic := range diagnostics {
			if diagnostic.Severity == planner.DiagnosticError {
				return rejected(diagnostic.Reason, "%s", diagnostic.Message)
			}
		}
		if errors.Is(err, planner.ErrRejected) {
			return rejected("PlannerRejected", "shutdown flow planner rejected structural inputs")
		}
		return rejected("PlannerFailed", "shutdown flow planner failed: %v", err)
	}

	if obj.Spec.Mode == powerv1alpha1.ShutdownFlowModeEnforce {
		approvalAnnotation := obj.Spec.Safety.ApprovalAnnotation
		if approvalAnnotation == "" {
			return rejected("ApprovalAnnotationRequired", "Enforce mode requires spec.safety.approvalAnnotation")
		}
		if obj.Annotations[approvalAnnotation] != "true" {
			return rejected("FlowNotApproved", "Enforce mode requires approval annotation %q=true", approvalAnnotation)
		}
	}

	return accepted("shutdown flow contract accepted")
}

// validateShutdownFlowDeviceIdentification blocks Enforce mode when a UPS the
// flow depends on matched no product capability profile.
//
// An unidentified device is not a device with reduced capability. It is a
// device nothing is known about: some NUT driver answered, and that is all. UPS
// hardware differs too much for that to be a safe basis for powering real nodes
// off, so enforcement is refused until either a profile matches or an operator
// records acceptance in Git via spec.safety.allowUnidentifiedDevices.
//
// This is a configuration-time refusal, deliberately: it is visible in status
// and in review long before an outage, which is the opposite of the mid-outage
// refusal PL-31 warns against.
func validateShutdownFlowDeviceIdentification(obj *powerv1alpha1.ShutdownFlow, bundle resolver.StructuralBundle) validationResult {
	if obj.Spec.Mode != powerv1alpha1.ShutdownFlowModeEnforce {
		return accepted("shutdown flow contract accepted")
	}
	if obj.Spec.Safety.AllowUnidentifiedDevices != nil && *obj.Spec.Safety.AllowUnidentifiedDevices {
		return accepted("shutdown flow contract accepted")
	}

	scope := shutdownFlowDeviceScope(obj, bundle)
	var unidentified []string
	for _, match := range bundle.CapabilityMatches {
		if !match.Unidentified {
			continue
		}
		if _, inScope := scope[match.DeviceID]; !inScope && len(scope) > 0 {
			continue
		}
		unidentified = append(unidentified, match.DeviceID)
	}
	if len(unidentified) == 0 {
		return accepted("shutdown flow contract accepted")
	}
	sort.Strings(unidentified)

	return rejected("UnidentifiedUPSDevice",
		"Enforce mode is blocked because %s matched no capability profile. "+
			"Nothing has been verified about what these devices report, so the operator will not power nodes off on their signal. "+
			"Create a UPSCapabilityProbe to draft a profile, or set spec.safety.allowUnidentifiedDevices to accept the risk",
		strings.Join(unidentified, ", "))
}

// shutdownFlowDeviceScope resolves which UPS devices this flow's triggers
// depend on. An empty result means the flow names neither devices nor domains,
// in which case every device is in scope.
func shutdownFlowDeviceScope(obj *powerv1alpha1.ShutdownFlow, bundle resolver.StructuralBundle) map[string]struct{} {
	scope := map[string]struct{}{}
	domains := map[string][]string{}
	for _, domain := range bundle.Topology.Domains {
		domains[domain.Name] = domain.UPSDevices
	}

	for _, trigger := range obj.Spec.Triggers {
		for _, device := range trigger.UPSDeviceRefs {
			scope[device.Name] = struct{}{}
		}
		for _, name := range trigger.PowerDomains {
			for _, device := range domains[name] {
				scope[device] = struct{}{}
			}
		}
	}
	return scope
}

func validatePowerInfrastructure(obj *powerv1alpha1.PowerInfrastructure) validationResult {
	return accepted("power infrastructure inventory contract accepted")
}

func validatePowerInventoryNode(obj *powerv1alpha1.PowerInventoryNode) validationResult {
	if obj.Spec.NodeName == "" {
		return rejected("NodeNameRequired", "spec.nodeName is required")
	}
	if obj.Spec.Roles.ShutdownTier != nil && *obj.Spec.Roles.ShutdownTier < 1 {
		return rejected("ShutdownTierZeroNode", "spec.roles.shutdownTier must be 1 or greater because tier 0 is workload-only")
	}
	return accepted("power inventory node contract accepted")
}

func validatePowerInventoryEdge(obj *powerv1alpha1.PowerInventoryEdge) validationResult {
	if obj.Spec.From.Kind == "" || obj.Spec.From.Name == "" {
		return rejected("FromRequired", "spec.from.kind and spec.from.name are required")
	}
	if obj.Spec.To.Kind == "" || obj.Spec.To.Name == "" {
		return rejected("ToRequired", "spec.to.kind and spec.to.name are required")
	}
	if obj.Spec.From.Kind == obj.Spec.To.Kind && obj.Spec.From.Name == obj.Spec.To.Name {
		return rejected("SelfEdge", "inventory edge %s/%s cannot reference itself", obj.Spec.From.Kind, obj.Spec.From.Name)
	}
	switch obj.Spec.Relation {
	case powerv1alpha1.PowerInventoryEdgeFeeds:
		if obj.Spec.Input == "" {
			return rejected("FeedInputRequired", "Feeds edges require spec.input to identify the target power input")
		}
	case powerv1alpha1.PowerInventoryEdgeCarries:
	default:
		return rejected("UnsupportedEdgeRelation", "unsupported inventory edge relation %q", obj.Spec.Relation)
	}
	if !isSupportedInventoryEntityKind(obj.Spec.From.Kind) {
		return rejected("UnsupportedEntityKind", "unsupported spec.from.kind %q", obj.Spec.From.Kind)
	}
	if !isSupportedInventoryEntityKind(obj.Spec.To.Kind) {
		return rejected("UnsupportedEntityKind", "unsupported spec.to.kind %q", obj.Spec.To.Kind)
	}
	return accepted("power inventory edge contract accepted")
}

func isSupportedInventoryEntityKind(kind powerv1alpha1.PowerInventoryEntityKind) bool {
	switch kind {
	case powerv1alpha1.PowerInventoryEntityUPSDevice,
		powerv1alpha1.PowerInventoryEntityNode,
		powerv1alpha1.PowerInventoryEntityPowerInfrastructure:
		return true
	default:
		return false
	}
}

func validateUPSCapabilityProfile(obj *powerv1alpha1.UPSCapabilityProfile) validationResult {
	selector := obj.Spec.Selector
	return validateCapabilityProfileContract("UPS", obj.Spec.Version, capabilityProfileSelectorFields{
		Model:        selector.Model,
		Firmware:     selector.Firmware,
		ModelGlob:    selector.ModelGlob,
		DriverFamily: selector.DriverFamily,
		Universal:    selector.Universal != nil && *selector.Universal,
	})
}

// capabilityProfileSelectorFields is the selector shape both profile kinds share.
//
// OD-25 calls for the precedence chain and semver rules to be factored rather than
// duplicated when the PDU kind arrives. Two copies of "exactly one match tier, and
// firmware only narrows a model" would agree on the day they were written and
// diverge the first time either was corrected.
type capabilityProfileSelectorFields struct {
	Model        string
	Firmware     string
	ModelGlob    string
	DriverFamily string
	Universal    bool
}

func validateCapabilityProfileContract(kind string, version string, selector capabilityProfileSelectorFields) validationResult {
	if version == "" {
		return rejected("ProfileVersionRequired", "spec.version is required")
	}
	if !semanticVersionPattern.MatchString(version) {
		return rejected("ProfileVersionInvalid", "spec.version %q must be semantic version x.y.z, optionally prefixed with v and with prerelease/build metadata", version)
	}
	universal := selector.Universal
	selectorCount := 0
	for _, value := range []string{selector.Model, selector.ModelGlob, selector.DriverFamily} {
		if value != "" {
			selectorCount++
		}
	}
	if universal {
		selectorCount++
	}
	if selectorCount == 0 {
		return rejected("ProfileSelectorRequired", "spec.selector requires model, modelGlob, driverFamily, or universal")
	}
	if universal && selectorCount > 1 {
		return rejected("UniversalSelectorExclusive", "universal capability profiles cannot include model, modelGlob, or driverFamily selectors")
	}
	if selectorCount > 1 {
		return rejected("AmbiguousProfileSelector", "capability profiles require exactly one match tier; firmware may only narrow an exact model selector")
	}
	if selector.Firmware != "" && selector.Model == "" {
		return rejected("FirmwareRequiresModel", "spec.selector.firmware requires spec.selector.model")
	}
	if selector.Model != "" && selector.ModelGlob != "" {
		return rejected("AmbiguousProfileSelector", "spec.selector.model and spec.selector.modelGlob cannot both be set")
	}
	if selector.ModelGlob != "" {
		if _, err := path.Match(selector.ModelGlob, "probe"); err != nil {
			return rejected("InvalidModelGlob", "spec.selector.modelGlob %q is invalid", selector.ModelGlob)
		}
	}
	return accepted(kind + " capability profile contract accepted")
}

// validatePDUCapabilityProfile validates the parallel PDU kind (OD-25).
//
// The selector contract is shared with UPS profiles. What is not shared is the
// outlet declaration, which has no UPS equivalent.
func validatePDUCapabilityProfile(obj *powerv1alpha1.PDUCapabilityProfile) validationResult {
	selector := obj.Spec.Selector
	result := validateCapabilityProfileContract("PDU", obj.Spec.Version, capabilityProfileSelectorFields{
		Model:        selector.Model,
		Firmware:     selector.Firmware,
		ModelGlob:    selector.ModelGlob,
		DriverFamily: selector.DriverFamily,
		Universal:    selector.Universal != nil && *selector.Universal,
	})
	if !result.accepted {
		return result
	}
	return validatePDUOutletDeclaration(obj.Spec.Outlets)
}

// validatePDUOutletDeclaration rejects outlet declarations that contradict
// themselves.
//
// More switchable outlets than outlets means the author is describing hardware
// they have not counted. Rejected rather than trimmed, because silently narrowing
// a capability declaration is how a device ends up believed less capable than it
// is with nothing saying so.
func validatePDUOutletDeclaration(outlets powerv1alpha1.PDUCapabilityOutletSpec) validationResult {
	if outlets.Count > 0 && int(outlets.Count) < len(outlets.Switchable) {
		return rejected("PDUOutletCountMismatch",
			"spec.outlets declares %d switchable outlets but only %d outlets", len(outlets.Switchable), outlets.Count)
	}
	seen := map[string]struct{}{}
	for _, outlet := range outlets.Switchable {
		if outlet == "" {
			return rejected("PDUOutletNameRequired", "spec.outlets.switchable entries cannot be empty")
		}
		if _, duplicate := seen[outlet]; duplicate {
			return rejected("DuplicatePDUOutlet", "spec.outlets.switchable lists %q more than once", outlet)
		}
		seen[outlet] = struct{}{}
	}
	return accepted("PDU capability profile contract accepted")
}

// reservedOperandNamespaces mirrors internal/webhook/v1alpha1's list of namespaces this operator must
// never be pointed at as an operand namespace (F-4). Duplicated rather than imported: the webhook
// package depends on the API/controller packages, not the reverse, and this is the same small,
// controlled duplication already accepted elsewhere in this codebase (isSupportedInventoryEntityKind).
// The webhook is the primary defense (rejects the request at admission time); this is belt-and-
// suspenders for objects that predate the webhook or reach the controller with it bypassed.
var reservedOperandNamespaces = map[string]bool{
	"default":         true,
	"kube-system":     true,
	"kube-public":     true,
	"kube-node-lease": true,
}

func rejectReservedOperandNamespace(name string) error {
	if reservedOperandNamespaces[name] {
		return fmt.Errorf("operand namespace %q is a reserved Kubernetes system namespace", name)
	}
	return nil
}
