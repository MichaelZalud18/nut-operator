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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
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
	if len(obj.Spec.Triggers) == 0 {
		return rejected("TriggersRequired", "spec.triggers requires at least one trigger")
	}
	if len(obj.Spec.Groups) == 0 && len(obj.Spec.Steps) == 0 {
		return rejected("PlanRequired", "spec.groups or spec.steps requires at least one shutdown action")
	}

	seen := map[string]struct{}{}
	for _, step := range obj.Spec.Steps {
		if step.ID == "" {
			return rejected("StepIDRequired", "every shutdown step requires an id")
		}
		if _, exists := seen[step.ID]; exists {
			return rejected("DuplicateStepID", "shutdown step id %q is duplicated", step.ID)
		}
		seen[step.ID] = struct{}{}
	}

	groupNames := map[string]struct{}{}
	for _, group := range obj.Spec.Groups {
		if group.Name == "" {
			return rejected("GroupNameRequired", "every shutdown group requires a name")
		}
		if _, exists := groupNames[group.Name]; exists {
			return rejected("DuplicateGroupName", "shutdown group name %q is duplicated", group.Name)
		}
		groupNames[group.Name] = struct{}{}
	}
	for _, group := range obj.Spec.Groups {
		for _, dependency := range append(append([]string{}, group.Requires...), append(group.Before, group.After...)...) {
			if _, exists := groupNames[dependency]; !exists {
				return rejected("UnknownDependency", "shutdown group %q references unknown dependency %q", group.Name, dependency)
			}
		}
	}
	if hasShutdownGroupCycle(obj.Spec.Groups) {
		return rejected("DependencyCycle", "shutdown groups contain a dependency cycle")
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

func hasShutdownGroupCycle(groups []powerv1alpha1.ShutdownGroup) bool {
	edges := shutdownGroupEdges(groups)
	visiting := map[string]bool{}
	visited := map[string]bool{}

	var visit func(string) bool
	visit = func(name string) bool {
		if visiting[name] {
			return true
		}
		if visited[name] {
			return false
		}
		visiting[name] = true
		for _, next := range edges[name] {
			if visit(next) {
				return true
			}
		}
		visiting[name] = false
		visited[name] = true
		return false
	}

	for _, group := range groups {
		if visit(group.Name) {
			return true
		}
	}
	return false
}

func shutdownGroupEdges(groups []powerv1alpha1.ShutdownGroup) map[string][]string {
	edges := map[string][]string{}
	for _, group := range groups {
		edges[group.Name] = append(edges[group.Name], group.Requires...)
		edges[group.Name] = append(edges[group.Name], group.Before...)
		for _, after := range group.After {
			edges[after] = append(edges[after], group.Name)
		}
	}
	return edges
}

func compileShutdownFlow(obj *powerv1alpha1.ShutdownFlow) ([]powerv1alpha1.CompiledShutdownStep, []powerv1alpha1.CompiledShutdownWave, *metav1.Duration) {
	if len(obj.Spec.Groups) == 0 {
		steps, duration := compileShutdownSteps(obj.Spec.Steps)
		return steps, nil, duration
	}

	steps, waves, duration := compileShutdownGroups(obj.Spec.Groups)
	return steps, waves, duration
}

func compileShutdownGroups(groups []powerv1alpha1.ShutdownGroup) ([]powerv1alpha1.CompiledShutdownStep, []powerv1alpha1.CompiledShutdownWave, *metav1.Duration) {
	byName := map[string]powerv1alpha1.ShutdownGroup{}
	indegree := map[string]int{}
	edges := shutdownGroupEdges(groups)
	for _, group := range groups {
		byName[group.Name] = group
		indegree[group.Name] = 0
	}
	for _, successors := range edges {
		for _, successor := range successors {
			indegree[successor]++
		}
	}

	var waves []powerv1alpha1.CompiledShutdownWave
	var steps []powerv1alpha1.CompiledShutdownStep
	var cumulative time.Duration
	var stepIndex int32
	var waveIndex int32

	for len(indegree) > 0 {
		ready := make([]powerv1alpha1.ShutdownGroup, 0)
		for name, count := range indegree {
			if count == 0 {
				ready = append(ready, byName[name])
			}
		}
		sort.Slice(ready, func(i, j int) bool {
			phaseI, phaseJ := groupPhase(ready[i]), groupPhase(ready[j])
			if phaseI != phaseJ {
				return phaseI < phaseJ
			}
			return ready[i].Name < ready[j].Name
		})

		phase := groupPhase(ready[0])
		waveGroups := make([]powerv1alpha1.ShutdownGroup, 0, len(ready))
		for _, group := range ready {
			if groupPhase(group) == phase {
				waveGroups = append(waveGroups, group)
			}
		}

		var waveDuration time.Duration
		for _, group := range waveGroups {
			if group.Timeout != nil && group.Timeout.Duration > waveDuration {
				waveDuration = group.Timeout.Duration
			}
		}

		groupNames := make([]string, 0, len(waveGroups))
		for _, group := range waveGroups {
			groupNames = append(groupNames, group.Name)
			duration := metav1.Duration{Duration: cumulative + waveDuration}
			steps = append(steps, powerv1alpha1.CompiledShutdownStep{
				ID:                 group.Name,
				Index:              stepIndex,
				Type:               group.Action,
				TargetSummary:      summarizeStepTarget(group.Target),
				CumulativeDuration: &duration,
			})
			stepIndex++
		}

		cumulative += waveDuration
		duration := metav1.Duration{Duration: waveDuration}
		cumulativeDuration := metav1.Duration{Duration: cumulative}
		wave := powerv1alpha1.CompiledShutdownWave{
			Index:              waveIndex,
			Groups:             groupNames,
			Duration:           &duration,
			CumulativeDuration: &cumulativeDuration,
		}
		if phase != 0 {
			phaseCopy := phase
			wave.Phase = &phaseCopy
		}
		waves = append(waves, wave)
		waveIndex++

		for _, group := range waveGroups {
			delete(indegree, group.Name)
			for _, successor := range edges[group.Name] {
				indegree[successor]--
			}
		}
	}

	total := metav1.Duration{Duration: cumulative}
	return steps, waves, &total
}

func groupPhase(group powerv1alpha1.ShutdownGroup) int32 {
	if group.Phase == nil {
		return 0
	}
	return *group.Phase
}

func compileShutdownSteps(steps []powerv1alpha1.ShutdownStep) ([]powerv1alpha1.CompiledShutdownStep, *metav1.Duration) {
	compiled := make([]powerv1alpha1.CompiledShutdownStep, 0, len(steps))
	var cumulative time.Duration

	for i, step := range steps {
		if step.Duration != nil {
			cumulative += step.Duration.Duration
		}
		if step.Timeout != nil && step.Type != powerv1alpha1.ShutdownStepWait {
			cumulative += step.Timeout.Duration
		}

		duration := metav1.Duration{Duration: cumulative}
		compiled = append(compiled, powerv1alpha1.CompiledShutdownStep{
			ID:                 step.ID,
			Index:              int32(i),
			Type:               step.Type,
			TargetSummary:      summarizeStepTarget(step.Target),
			CumulativeDuration: &duration,
		})
	}

	total := metav1.Duration{Duration: cumulative}
	return compiled, &total
}

func summarizeStepTarget(target powerv1alpha1.ShutdownStepTarget) string {
	switch {
	case target.NodeSelector != nil:
		return "nodes selected by label selector"
	case target.NamespaceSelector != nil:
		return "namespaces selected by label selector"
	case target.WorkloadSelector != nil:
		return "workloads selected by label selector"
	case len(target.AgentRefs) > 0:
		return fmt.Sprintf("%d node power agent fleet(s)", len(target.AgentRefs))
	case len(target.WorkloadRefs) > 0:
		return fmt.Sprintf("%d workload(s)", len(target.WorkloadRefs))
	case len(target.Namespaces) > 0:
		return fmt.Sprintf("%d namespace(s)", len(target.Namespaces))
	default:
		return "no explicit target"
	}
}
