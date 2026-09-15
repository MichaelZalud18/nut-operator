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
	"fmt"
	"path"
	"regexp"
	"strings"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

// ValidationResult carries the existing inventory contract verdict to diagnostics
// and controller conditions without depending on either publishing mechanism.
type ValidationResult struct {
	Accepted bool
	Reason   string
	Message  string
}

var semanticVersionPattern = regexp.MustCompile(`^v?[0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)

func accepted(message string) ValidationResult {
	return ValidationResult{Accepted: true, Reason: "Accepted", Message: message}
}

func rejected(reason, format string, args ...any) ValidationResult {
	return ValidationResult{Accepted: false, Reason: reason, Message: fmt.Sprintf(format, args...)}
}

func ValidateUPSDevice(obj *powerv1alpha1.UPSDevice) ValidationResult {
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
	// F-85. `driver` was reserved only on the upstreamNUT path, so a non-upstream device could set
	// it in driverOptions and renderUPSConf would emit a second `driver =` line below the one it
	// renders from spec.driver -- and ups.conf takes the last. That walks straight around the
	// allowlist three lines up, which is what keeps the API network-only (RB-1, RB-2), turning an
	// admission rejection into a driver that fails at runtime instead.
	if _, exists := obj.Spec.DriverOptions["driver"]; exists {
		return rejected("ReservedDriverOption", "spec.driverOptions[%q] is rendered from spec.driver and cannot be overridden", "driver")
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

func validateUpstreamNUTUPSDevice(obj *powerv1alpha1.UPSDevice) ValidationResult {
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
	authMode := UpstreamAuthMode(upstream)
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

func UpstreamAuthMode(upstream *powerv1alpha1.UPSUpstreamNUTSpec) powerv1alpha1.UPSUpstreamNUTAuthMode {
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

func ValidatePowerInfrastructure(obj *powerv1alpha1.PowerInfrastructure) ValidationResult {
	return accepted("power infrastructure inventory contract accepted")
}

func ValidatePowerInventoryNode(obj *powerv1alpha1.PowerInventoryNode) ValidationResult {
	if obj.Spec.NodeName == "" {
		return rejected("NodeNameRequired", "spec.nodeName is required")
	}
	if obj.Spec.Roles.ShutdownTier != nil && *obj.Spec.Roles.ShutdownTier < 1 {
		return rejected("ShutdownTierZeroNode", "spec.roles.shutdownTier must be 1 or greater because tier 0 is workload-only")
	}
	return accepted("power inventory node contract accepted")
}

func ValidatePowerInventoryEdge(obj *powerv1alpha1.PowerInventoryEdge) ValidationResult {
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
	if !IsSupportedEntityKind(obj.Spec.From.Kind) {
		return rejected("UnsupportedEntityKind", "unsupported spec.from.kind %q", obj.Spec.From.Kind)
	}
	if !IsSupportedEntityKind(obj.Spec.To.Kind) {
		return rejected("UnsupportedEntityKind", "unsupported spec.to.kind %q", obj.Spec.To.Kind)
	}
	return accepted("power inventory edge contract accepted")
}

func IsSupportedEntityKind(kind powerv1alpha1.PowerInventoryEntityKind) bool {
	switch kind {
	case powerv1alpha1.PowerInventoryEntityUPSDevice,
		powerv1alpha1.PowerInventoryEntityNode,
		powerv1alpha1.PowerInventoryEntityPowerInfrastructure:
		return true
	default:
		return false
	}
}

func ValidateUPSCapabilityProfile(obj *powerv1alpha1.UPSCapabilityProfile) ValidationResult {
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

func validateCapabilityProfileContract(kind string, version string, selector capabilityProfileSelectorFields) ValidationResult {
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

// ValidatePDUCapabilityProfile validates the parallel PDU kind (OD-25).
//
// The selector contract is shared with UPS profiles. What is not shared is the
// outlet declaration, which has no UPS equivalent.
func ValidatePDUCapabilityProfile(obj *powerv1alpha1.PDUCapabilityProfile) ValidationResult {
	selector := obj.Spec.Selector
	result := validateCapabilityProfileContract("PDU", obj.Spec.Version, capabilityProfileSelectorFields{
		Model:        selector.Model,
		Firmware:     selector.Firmware,
		ModelGlob:    selector.ModelGlob,
		DriverFamily: selector.DriverFamily,
		Universal:    selector.Universal != nil && *selector.Universal,
	})
	if !result.Accepted {
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
func validatePDUOutletDeclaration(outlets powerv1alpha1.PDUCapabilityOutletSpec) ValidationResult {
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
