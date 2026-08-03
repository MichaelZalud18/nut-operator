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
	steps, waves, duration, hash, _ := CompileArtifact(obj)
	return steps, waves, duration, hash
}

// CompileArtifact returns the Kubernetes status-shaped view of a ShutdownFlow plan and its published artifact.
func CompileArtifact(obj *powerv1alpha1.ShutdownFlow) ([]powerv1alpha1.CompiledShutdownStep, []powerv1alpha1.CompiledShutdownWave, *metav1.Duration, string, *powerv1alpha1.PublishedPlannerArtifactStatus) {
	plan, _, err := planner.Compile(PlannerInputs(obj), planner.TelemetryInputs{})
	if err != nil {
		return nil, nil, nil, "", nil
	}

	return APICompiledSteps(plan.Steps), APICompiledWaves(plan.Waves), APIDuration(plan.EstimatedDuration), plan.Hash, APIPlannerArtifact(plan)
}

// CompileWithResolvedInputs includes resolved inventory and capability identity in the plan hash.
func CompileWithResolvedInputs(obj *powerv1alpha1.ShutdownFlow, bundle resolver.StructuralBundle) ([]powerv1alpha1.CompiledShutdownStep, []powerv1alpha1.CompiledShutdownWave, *metav1.Duration, string) {
	steps, waves, duration, hash, _ := CompileArtifactWithResolvedInputs(obj, bundle)
	return steps, waves, duration, hash
}

// CompileArtifactWithResolvedInputs includes resolved inventory and capability identity in the plan hash and artifact.
func CompileArtifactWithResolvedInputs(obj *powerv1alpha1.ShutdownFlow, bundle resolver.StructuralBundle) ([]powerv1alpha1.CompiledShutdownStep, []powerv1alpha1.CompiledShutdownWave, *metav1.Duration, string, *powerv1alpha1.PublishedPlannerArtifactStatus) {
	inputs := resolver.AttachResolvedInputHash(PlannerInputs(obj), bundle)
	plan, _, err := planner.Compile(inputs, planner.TelemetryInputs{})
	if err != nil {
		return nil, nil, nil, "", nil
	}

	return APICompiledSteps(plan.Steps), APICompiledWaves(plan.Waves), APIDuration(plan.EstimatedDuration), plan.Hash, APIPlannerArtifact(plan)
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

// APIPlannerArtifact converts the pure planner artifact into the compact ShutdownFlow status shape.
func APIPlannerArtifact(plan planner.Plan) *powerv1alpha1.PublishedPlannerArtifactStatus {
	return &powerv1alpha1.PublishedPlannerArtifactStatus{
		Graph:        APIPlannerGraph(plan.Graph),
		StartupWaves: APICompiledWaves(plan.StartupWaves),
		Explanations: APIPlannerExplanations(plan.Explanations),
		Diagrams: powerv1alpha1.PlannerDiagramExportsStatus{
			Mermaid:     plan.Diagrams.Mermaid,
			GraphvizDOT: plan.Diagrams.GraphvizDOT,
			D2:          plan.Diagrams.D2,
		},
	}
}

// APIPlannerGraph converts a pure planner graph into the ShutdownFlow status shape.
func APIPlannerGraph(graph planner.Graph) powerv1alpha1.PlannerGraphStatus {
	status := powerv1alpha1.PlannerGraphStatus{
		Vertices: make([]powerv1alpha1.PlannerGraphVertexStatus, 0, len(graph.Vertices)),
		Edges:    make([]powerv1alpha1.PlannerGraphEdgeStatus, 0, len(graph.Edges)),
	}
	for _, vertex := range graph.Vertices {
		status.Vertices = append(status.Vertices, powerv1alpha1.PlannerGraphVertexStatus{
			ID:            vertex.ID,
			Kind:          vertex.Kind,
			Label:         vertex.Label,
			Action:        vertex.Action,
			Phase:         vertex.Phase,
			TargetSummary: vertex.TargetSummary,
		})
	}
	for _, edge := range graph.Edges {
		status.Edges = append(status.Edges, powerv1alpha1.PlannerGraphEdgeStatus{
			ID:          edge.ID,
			From:        edge.From,
			To:          edge.To,
			Relation:    edge.Relation,
			Provenance:  edge.Provenance,
			Sources:     APIPlannerGraphSources(edge.Sources),
			Explanation: edge.Explanation,
		})
	}
	return status
}

// APIPlannerGraphSources converts pure graph source refs into the ShutdownFlow status shape.
func APIPlannerGraphSources(sources []planner.GraphSourceRef) []powerv1alpha1.PlannerGraphSourceRefStatus {
	if len(sources) == 0 {
		return nil
	}
	status := make([]powerv1alpha1.PlannerGraphSourceRefStatus, 0, len(sources))
	for _, source := range sources {
		status = append(status, powerv1alpha1.PlannerGraphSourceRefStatus{
			Kind:  source.Kind,
			Name:  source.Name,
			Field: source.Field,
		})
	}
	return status
}

// APIPlannerExplanations converts pure planner explanations into the ShutdownFlow status shape.
func APIPlannerExplanations(explanations []planner.Explanation) []powerv1alpha1.PlannerExplanationStatus {
	if len(explanations) == 0 {
		return nil
	}
	status := make([]powerv1alpha1.PlannerExplanationStatus, 0, len(explanations))
	for _, explanation := range explanations {
		status = append(status, powerv1alpha1.PlannerExplanationStatus{
			ID:      explanation.ID,
			Subject: explanation.Subject,
			Reason:  explanation.Reason,
			Message: explanation.Message,
		})
	}
	return status
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
