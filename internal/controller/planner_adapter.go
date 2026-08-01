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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
)

func compileShutdownFlow(obj *powerv1alpha1.ShutdownFlow) ([]powerv1alpha1.CompiledShutdownStep, []powerv1alpha1.CompiledShutdownWave, *metav1.Duration, string) {
	plan, _, err := planner.Compile(plannerInputsFromShutdownFlow(obj), planner.TelemetryInputs{})
	if err != nil {
		return nil, nil, nil, ""
	}

	return apiCompiledSteps(plan.Steps), apiCompiledWaves(plan.Waves), apiDuration(plan.EstimatedDuration), plan.Hash
}

func compileShutdownFlowWithResolvedInputs(obj *powerv1alpha1.ShutdownFlow, bundle resolver.StructuralBundle) ([]powerv1alpha1.CompiledShutdownStep, []powerv1alpha1.CompiledShutdownWave, *metav1.Duration, string) {
	inputs := resolver.AttachResolvedInputHash(plannerInputsFromShutdownFlow(obj), bundle)
	plan, _, err := planner.Compile(inputs, planner.TelemetryInputs{})
	if err != nil {
		return nil, nil, nil, ""
	}

	return apiCompiledSteps(plan.Steps), apiCompiledWaves(plan.Waves), apiDuration(plan.EstimatedDuration), plan.Hash
}

func plannerInputsFromShutdownFlow(obj *powerv1alpha1.ShutdownFlow) planner.StructuralInputs {
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
			For:                 plannerDuration(trigger.For),
			RuntimeBelowSeconds: trigger.RuntimeBelowSeconds,
			ChargeBelowPercent:  trigger.ChargeBelowPercent,
		})
	}
	for _, group := range obj.Spec.Groups {
		inputs.Groups = append(inputs.Groups, planner.Group{
			Name:        group.Name,
			Description: group.Description,
			Action:      string(group.Action),
			Target:      plannerTarget(group.Target),
			Requires:    append([]string(nil), group.Requires...),
			Before:      append([]string(nil), group.Before...),
			After:       append([]string(nil), group.After...),
			Phase:       group.Phase,
			Timeout:     plannerDuration(group.Timeout),
			Params:      copyParams(group.Params),
		})
	}
	for _, step := range obj.Spec.Steps {
		inputs.Steps = append(inputs.Steps, planner.Step{
			ID:              step.ID,
			Action:          string(step.Type),
			Target:          plannerTarget(step.Target),
			Duration:        plannerDuration(step.Duration),
			Timeout:         plannerDuration(step.Timeout),
			ContinueOnError: step.ContinueOnError != nil && *step.ContinueOnError,
			Params:          copyParams(step.Params),
		})
	}

	return inputs
}

func objectReferenceNames(refs []powerv1alpha1.ObjectNameReference) []string {
	names := make([]string, 0, len(refs))
	for _, ref := range refs {
		names = append(names, ref.Name)
	}
	return names
}

func plannerDuration(duration *metav1.Duration) planner.Duration {
	if duration == nil {
		return planner.Duration{}
	}
	return planner.Duration{Duration: duration.Duration}
}

func plannerTarget(target powerv1alpha1.ShutdownStepTarget) planner.Target {
	return planner.Target{
		NodeSelector:      target.NodeSelector != nil,
		NamespaceSelector: target.NamespaceSelector != nil,
		WorkloadSelector:  target.WorkloadSelector != nil,
		NamespaceCount:    len(target.Namespaces),
		WorkloadRefCount:  len(target.WorkloadRefs),
		AgentRefCount:     len(target.AgentRefs),
	}
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

func apiCompiledSteps(steps []planner.CompiledStep) []powerv1alpha1.CompiledShutdownStep {
	compiled := make([]powerv1alpha1.CompiledShutdownStep, 0, len(steps))
	for _, step := range steps {
		compiled = append(compiled, powerv1alpha1.CompiledShutdownStep{
			ID:                 step.ID,
			Index:              step.Index,
			Type:               powerv1alpha1.ShutdownStepType(step.Action),
			TargetSummary:      step.TargetSummary,
			CumulativeDuration: apiDuration(step.CumulativeDuration),
		})
	}
	return compiled
}

func apiCompiledWaves(waves []planner.Wave) []powerv1alpha1.CompiledShutdownWave {
	compiled := make([]powerv1alpha1.CompiledShutdownWave, 0, len(waves))
	for _, wave := range waves {
		compiled = append(compiled, powerv1alpha1.CompiledShutdownWave{
			Index:              wave.Index,
			Phase:              wave.Phase,
			Groups:             append([]string(nil), wave.Groups...),
			Duration:           apiDuration(wave.Duration),
			CumulativeDuration: apiDuration(wave.CumulativeDuration),
		})
	}
	return compiled
}

func apiDuration(duration planner.Duration) *metav1.Duration {
	return &metav1.Duration{Duration: duration.Duration}
}
