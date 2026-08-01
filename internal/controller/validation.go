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

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
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

func validateUPSDevice(obj *powerv1alpha1.UPSDevice) validationResult {
	if obj.Spec.Driver == "" {
		return rejected("DriverRequired", "spec.driver is required")
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

	return accepted("UPS device contract accepted")
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
	_, diagnostics, err := planner.Compile(plannerInputsFromShutdownFlow(obj), planner.TelemetryInputs{})
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
