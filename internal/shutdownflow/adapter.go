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

package shutdownflow

import (
	"fmt"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
)

// Compile returns the Kubernetes status-shaped view of a ShutdownFlow plan.
func Compile(obj *powerv1alpha1.ShutdownFlow) ([]powerv1alpha1.CompiledShutdownStep, []powerv1alpha1.CompiledShutdownWave, *metav1.Duration, string) {
	plan, _, err := planner.Compile(PlannerInputs(obj), planner.TelemetryInputs{})
	if err != nil {
		return nil, nil, nil, ""
	}

	return APICompiledSteps(plan.Steps), APICompiledWaves(plan.Waves), APIDuration(plan.EstimatedDuration), plan.Hash
}

// CompileWithResolvedInputs includes resolved inventory and capability identity in the plan hash.
func CompileWithResolvedInputs(obj *powerv1alpha1.ShutdownFlow, bundle resolver.StructuralBundle) ([]powerv1alpha1.CompiledShutdownStep, []powerv1alpha1.CompiledShutdownWave, *metav1.Duration, string) {
	inputs := resolver.AttachResolvedInputHash(PlannerInputs(obj), bundle)
	plan, _, err := planner.Compile(inputs, planner.TelemetryInputs{})
	if err != nil {
		return nil, nil, nil, ""
	}

	return APICompiledSteps(plan.Steps), APICompiledWaves(plan.Waves), APIDuration(plan.EstimatedDuration), plan.Hash
}

// PlannerInputs converts the Kubernetes API object into pure planner inputs.
func PlannerInputs(obj *powerv1alpha1.ShutdownFlow) planner.StructuralInputs {
	inputs := planner.StructuralInputs{
		SourceID:      fmt.Sprintf("%s/ShutdownFlow/%s", powerv1alpha1.GroupVersion.String(), obj.Name),
		AbortBehavior: string(obj.Spec.AbortPolicy.Behavior),
		Triggers:      make([]planner.Trigger, 0, len(obj.Spec.Triggers)),
		Groups:        make([]planner.Group, 0, len(obj.Spec.Groups)),
		Steps:         make([]planner.Step, 0, len(obj.Spec.Steps)),
	}

	for _, trigger := range obj.Spec.Triggers {
		inputs.Triggers = append(inputs.Triggers, planner.Trigger{
			Type:                string(trigger.Type),
			UPSDevices:          objectReferenceNames(trigger.UPSDeviceRefs),
			PowerDomains:        append([]string(nil), trigger.PowerDomains...),
			For:                 PlannerDuration(trigger.For),
			RuntimeBelowSeconds: trigger.RuntimeBelowSeconds,
			ChargeBelowPercent:  trigger.ChargeBelowPercent,
		})
	}
	for _, group := range obj.Spec.Groups {
		inputs.Groups = append(inputs.Groups, planner.Group{
			Name:        group.Name,
			Description: group.Description,
			Action:      string(group.Action),
			Target:      PlannerTarget(group.Target),
			Requires:    append([]string(nil), group.Requires...),
			Before:      append([]string(nil), group.Before...),
			After:       append([]string(nil), group.After...),
			Phase:       group.Phase,
			Timeout:     PlannerDuration(group.Timeout),
			Params:      copyParams(group.Params),
		})
	}
	for _, step := range obj.Spec.Steps {
		inputs.Steps = append(inputs.Steps, planner.Step{
			ID:              step.ID,
			Action:          string(step.Type),
			Target:          PlannerTarget(step.Target),
			Duration:        PlannerDuration(step.Duration),
			Timeout:         PlannerDuration(step.Timeout),
			ContinueOnError: step.ContinueOnError != nil && *step.ContinueOnError,
			Params:          copyParams(step.Params),
		})
	}

	return inputs
}

// PlannerDuration converts API durations into planner durations.
func PlannerDuration(duration *metav1.Duration) planner.Duration {
	if duration == nil {
		return planner.Duration{}
	}
	return planner.Duration{Duration: duration.Duration}
}

// PlannerTarget converts API target selectors into the planner's compact target summary.
func PlannerTarget(target powerv1alpha1.ShutdownStepTarget) planner.Target {
	return planner.Target{
		NodeSelector:      target.NodeSelector != nil,
		NamespaceSelector: target.NamespaceSelector != nil,
		WorkloadSelector:  target.WorkloadSelector != nil,
		NamespaceCount:    len(target.Namespaces),
		WorkloadRefCount:  len(target.WorkloadRefs),
		AgentRefCount:     len(target.AgentRefs),
	}
}

// APICompiledSteps converts planner steps into the ShutdownFlow status shape.
func APICompiledSteps(steps []planner.CompiledStep) []powerv1alpha1.CompiledShutdownStep {
	compiled := make([]powerv1alpha1.CompiledShutdownStep, 0, len(steps))
	for _, step := range steps {
		compiled = append(compiled, powerv1alpha1.CompiledShutdownStep{
			ID:                 step.ID,
			Index:              step.Index,
			Type:               powerv1alpha1.ShutdownStepType(step.Action),
			TargetSummary:      step.TargetSummary,
			CumulativeDuration: APIDuration(step.CumulativeDuration),
		})
	}
	return compiled
}

// APICompiledWaves converts planner waves into the ShutdownFlow status shape.
func APICompiledWaves(waves []planner.Wave) []powerv1alpha1.CompiledShutdownWave {
	compiled := make([]powerv1alpha1.CompiledShutdownWave, 0, len(waves))
	for _, wave := range waves {
		compiled = append(compiled, powerv1alpha1.CompiledShutdownWave{
			Index:              wave.Index,
			Phase:              wave.Phase,
			Groups:             append([]string(nil), wave.Groups...),
			Duration:           APIDuration(wave.Duration),
			CumulativeDuration: APIDuration(wave.CumulativeDuration),
		})
	}
	return compiled
}

// APIDuration converts planner durations into Kubernetes API durations.
func APIDuration(duration planner.Duration) *metav1.Duration {
	return &metav1.Duration{Duration: duration.Duration}
}

func objectReferenceNames(refs []powerv1alpha1.ObjectNameReference) []string {
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.Name)
	}
	return names
}

func copyParams(params map[string]string) map[string]string {
	if params == nil {
		return nil
	}
	copied := make(map[string]string, len(params))
	for key, value := range params {
		copied[key] = value
	}
	return copied
}
