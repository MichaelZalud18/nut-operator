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
	"fmt"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation/field"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/kubeinventory"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
	"github.com/MichaelZalud18/nut-operator/internal/resourcevalidation"
)

type validationResult struct {
	accepted bool
	reason   string
	message  string
}

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
	if result := validatePowerHookPolicy(obj.Spec.Hooks); !result.accepted {
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

func validatePowerHookPolicy(policy powerv1alpha1.PowerHookPolicySpec) validationResult {
	if policy.DefaultTimeout != nil && policy.DefaultTimeout.Duration <= 0 {
		return rejected("HookDefaultTimeoutInvalid", "spec.hooks.defaultTimeout must be greater than zero")
	}
	seen := map[string]struct{}{}
	for _, endpoint := range policy.AllowedEndpoints {
		if endpoint.Scheme != "http" && endpoint.Scheme != "https" {
			return rejected("HookEndpointSchemeUnsupported", "spec.hooks.allowedEndpoints only supports http and https schemes")
		}
		if endpoint.Host == "" {
			return rejected("HookEndpointHostRequired", "spec.hooks.allowedEndpoints requires every endpoint to name a host")
		}
		if endpoint.Port != nil && (*endpoint.Port < 1 || *endpoint.Port > 65535) {
			return rejected("HookEndpointPortInvalid", "spec.hooks.allowedEndpoints port must be between 1 and 65535")
		}
		if endpoint.PathPrefix != "" && !strings.HasPrefix(endpoint.PathPrefix, "/") {
			return rejected("HookEndpointPathPrefixInvalid", "spec.hooks.allowedEndpoints pathPrefix must start with /")
		}
		key := fmt.Sprintf("%s://%s:%d%s", endpoint.Scheme, endpoint.Host, optionalHookEndpointPort(endpoint.Port), endpoint.PathPrefix)
		if _, exists := seen[key]; exists {
			return rejected("DuplicateHookEndpoint", "spec.hooks.allowedEndpoints contains duplicate endpoint %s", key)
		}
		seen[key] = struct{}{}
	}
	return accepted("hook policy accepted")
}

func optionalHookEndpointPort(port *int32) int32 {
	if port == nil {
		return 0
	}
	return *port
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
	return staticValidationResult(resourcevalidation.ValidateNodePowerAgentFields(obj), "node power agent contract accepted")
}

func validateShutdownFlow(obj *powerv1alpha1.ShutdownFlow) validationResult {
	_, errs := resourcevalidation.ValidateShutdownFlowFields(obj)
	return staticValidationResult(errs, "shutdown flow contract accepted")
}

func staticValidationResult(errs field.ErrorList, message string) validationResult {
	if len(errs) == 0 {
		return accepted(message)
	}
	reason := errs[0].Origin
	if reason == "" {
		reason = "InvalidSpec"
	}
	return rejected(reason, "%s", errs[0].Error())
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

func rejectReservedOperandNamespace(name string) error {
	if resourcevalidation.IsReservedOperandNamespace(name) {
		return fmt.Errorf("operand namespace %q is a reserved Kubernetes system namespace", name)
	}
	return nil
}

func validateUPSDevice(obj *powerv1alpha1.UPSDevice) validationResult {
	result := kubeinventory.ValidateUPSDevice(obj)
	return validationResult{accepted: result.Accepted, reason: result.Reason, message: result.Message}
}

func validatePowerInfrastructure(obj *powerv1alpha1.PowerInfrastructure) validationResult {
	result := kubeinventory.ValidatePowerInfrastructure(obj)
	return validationResult{accepted: result.Accepted, reason: result.Reason, message: result.Message}
}

func validatePowerInventoryNode(obj *powerv1alpha1.PowerInventoryNode) validationResult {
	result := kubeinventory.ValidatePowerInventoryNode(obj)
	return validationResult{accepted: result.Accepted, reason: result.Reason, message: result.Message}
}

func validatePowerInventoryEdge(obj *powerv1alpha1.PowerInventoryEdge) validationResult {
	result := kubeinventory.ValidatePowerInventoryEdge(obj)
	return validationResult{accepted: result.Accepted, reason: result.Reason, message: result.Message}
}

func validateUPSCapabilityProfile(obj *powerv1alpha1.UPSCapabilityProfile) validationResult {
	result := kubeinventory.ValidateUPSCapabilityProfile(obj)
	return validationResult{accepted: result.Accepted, reason: result.Reason, message: result.Message}
}

func validatePDUCapabilityProfile(obj *powerv1alpha1.PDUCapabilityProfile) validationResult {
	result := kubeinventory.ValidatePDUCapabilityProfile(obj)
	return validationResult{accepted: result.Accepted, reason: result.Reason, message: result.Message}
}
