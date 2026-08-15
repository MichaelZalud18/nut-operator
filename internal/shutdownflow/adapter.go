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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

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
	return CompileArtifactWithResolvedInputsAndTierPolicy(obj, bundle, powerv1alpha1.PowerShutdownTierPolicySpec{})
}

// CompileArtifactWithResolvedInputsAndTierPolicy includes resolved inventory, capability identity, and central tier policy.
func CompileArtifactWithResolvedInputsAndTierPolicy(obj *powerv1alpha1.ShutdownFlow, bundle resolver.StructuralBundle, tierPolicy powerv1alpha1.PowerShutdownTierPolicySpec) ([]powerv1alpha1.CompiledShutdownStep, []powerv1alpha1.CompiledShutdownWave, *metav1.Duration, string, *powerv1alpha1.PublishedPlannerArtifactStatus) {
	compiled := CompileFlow(obj, bundle, tierPolicy)
	return compiled.Steps, compiled.Waves, compiled.EstimatedDuration, compiled.ConfigHash, compiled.Artifact
}

// CompiledFlow is a compiled plan in Kubernetes-shaped form, together with the
// planner diagnostics that produced it.
//
// The diagnostics matter as much as the plan. A tier that was defaulted rather
// than declared, or a workload scheduled to outlive the node under it, is
// something the planner knows and nobody could previously see: every caller
// discarded this return value, so the planner's own findings ended at the
// function boundary.
type CompiledFlow struct {
	Steps               []powerv1alpha1.CompiledShutdownStep
	Waves               []powerv1alpha1.CompiledShutdownWave
	EstimatedDuration   *metav1.Duration
	ConfigHash          string
	Artifact            *powerv1alpha1.PublishedPlannerArtifactStatus
	Diagnostics         []planner.Diagnostic
	BlockedNodeReleases []powerv1alpha1.BlockedNodeReleaseStatus
	// GroupEstimates and EstimateConfidence carry the EX-32 provenance through to the
	// controller, which is what turns the OD-12 warning from a bare number into a
	// statement a reader can weigh.
	GroupEstimates     []planner.GroupEstimate
	EstimateConfidence planner.EstimateConfidence
	// ObservedDuration is the plan total from measured executions, nil until something
	// has run. The OD-12 warning prefers it over the declared estimate.
	ObservedDuration *metav1.Duration
}

// CompileFlow compiles a ShutdownFlow against resolved inventory, capability
// identity, and central tier policy, keeping the planner's diagnostics.
func CompileFlow(obj *powerv1alpha1.ShutdownFlow, bundle resolver.StructuralBundle, tierPolicy powerv1alpha1.PowerShutdownTierPolicySpec) CompiledFlow {
	return CompileFlowWithHistory(obj, bundle, tierPolicy, planner.HistoryInputs{})
}

// CompileFlowWithHistory compiles with observed durations from previous executions
// folded into the estimates (EX-32). CompileFlow is this with no history, which is
// what a cluster that has never run this flow legitimately has.
func CompileFlowWithHistory(obj *powerv1alpha1.ShutdownFlow, bundle resolver.StructuralBundle, tierPolicy powerv1alpha1.PowerShutdownTierPolicySpec, history planner.HistoryInputs) CompiledFlow {
	return CompileFlowWithHistoryAndHooks(obj, bundle, tierPolicy, history, nil)
}

// CompileFlowWithHistoryAndHooks compiles with observed durations and resolved
// hook identity folded into the structural plan hash.
func CompileFlowWithHistoryAndHooks(obj *powerv1alpha1.ShutdownFlow, bundle resolver.StructuralBundle, tierPolicy powerv1alpha1.PowerShutdownTierPolicySpec, history planner.HistoryInputs, hookDigests []planner.HookDigest) CompiledFlow {
	inputs := resolver.AttachResolvedInputHash(PlannerInputsWithTierPolicy(obj, tierPolicy), bundle)
	inputs.HookDigests = append([]planner.HookDigest(nil), hookDigests...)
	inputs.GroupNodes = PlannerGroupNodes(obj, bundle)
	plan, diagnostics, err := planner.CompileWithHistory(inputs, planner.TelemetryInputs{}, history)
	if err != nil {
		// Rejection diagnostics still travel: the reason a plan did not compile is
		// the most useful thing the planner produced.
		return CompiledFlow{Diagnostics: diagnostics}
	}

	return CompiledFlow{
		Steps:               APICompiledSteps(plan.Steps),
		Waves:               APICompiledWaves(plan.Waves),
		EstimatedDuration:   APIDuration(plan.EstimatedDuration),
		ConfigHash:          plan.Hash,
		Artifact:            APIPlannerArtifact(plan),
		Diagnostics:         diagnostics,
		BlockedNodeReleases: APIBlockedNodeReleases(plan.BlockedNodes),
		GroupEstimates:      plan.GroupEstimates,
		EstimateConfidence:  plan.EstimateConfidence,
		ObservedDuration:    APIDuration(plan.ObservedDuration),
	}
}

// APIBlockedNodeReleases converts the plan's withheld nodes to status form.
func APIBlockedNodeReleases(blocked []planner.BlockedNode) []powerv1alpha1.BlockedNodeReleaseStatus {
	if len(blocked) == 0 {
		return nil
	}
	statuses := make([]powerv1alpha1.BlockedNodeReleaseStatus, 0, len(blocked))
	for _, node := range blocked {
		statuses = append(statuses, powerv1alpha1.BlockedNodeReleaseStatus{
			NodeName: node.Name,
			Reason:   node.Reason,
			Groups:   append([]string(nil), node.Groups...),
			NodeTier: node.NodeTier,
			Message:  node.Message,
		})
	}
	return statuses
}

// PlannerInputs converts the Kubernetes API object into pure planner inputs.
func PlannerInputs(obj *powerv1alpha1.ShutdownFlow) planner.StructuralInputs {
	return PlannerInputsWithTierPolicy(obj, powerv1alpha1.PowerShutdownTierPolicySpec{})
}

// PlannerInputsWithTierPolicy converts the Kubernetes API object into pure planner inputs with central tier policy.
func PlannerInputsWithTierPolicy(obj *powerv1alpha1.ShutdownFlow, tierPolicy powerv1alpha1.PowerShutdownTierPolicySpec) planner.StructuralInputs {
	plannerTierPolicy := PlannerTierPolicy(tierPolicy)
	inputs := planner.StructuralInputs{
		SourceID:      fmt.Sprintf("%s/ShutdownFlow/%s", powerv1alpha1.GroupVersion.String(), obj.Name),
		TierPolicy:    plannerTierPolicy,
		AbortBehavior: string(obj.Spec.AbortPolicy.Behavior),
		Triggers:      make([]planner.Trigger, 0, len(obj.Spec.Triggers)),
		Groups:        make([]planner.Group, 0, len(obj.Spec.Groups)),
		Steps:         make([]planner.Step, 0, len(obj.Spec.Steps)),
	}

	for _, trigger := range obj.Spec.Triggers {
		inputs.Triggers = append(inputs.Triggers, planner.Trigger{
			Type:                string(trigger.Type),
			FallbackType:        trigger.FallbackType.Value(),
			UPSDevices:          objectReferenceNames(trigger.UPSDeviceRefs),
			PowerDomains:        append([]string(nil), trigger.PowerDomains...),
			For:                 PlannerDuration(trigger.For),
			RuntimeBelowSeconds: trigger.RuntimeBelowSeconds,
			ChargeBelowPercent:  trigger.ChargeBelowPercent,
		})
	}
	for _, group := range obj.Spec.Groups {
		inputs.Groups = append(inputs.Groups, planner.Group{
			Name:         group.Name,
			Description:  group.Description,
			Action:       string(group.Action),
			HookRef:      PlannerHookReference(group.HookRef),
			Target:       PlannerTarget(group.Target),
			Requires:     append([]string(nil), group.Requires...),
			Before:       append([]string(nil), group.Before...),
			After:        append([]string(nil), group.After...),
			Phase:        group.Phase,
			ShutdownTier: PlannerShutdownTier(group, tierPolicy),
			Timeout:      PlannerDuration(group.Timeout),
			Params:       copyParams(group.Params),

			TierInversionPolicy: string(group.TierInversionPolicy),
		})
	}
	for _, step := range obj.Spec.Steps {
		inputs.Steps = append(inputs.Steps, planner.Step{
			ID:              step.ID,
			Action:          string(step.Type),
			HookRef:         PlannerHookReference(step.HookRef),
			Target:          PlannerTarget(step.Target),
			Duration:        PlannerDuration(step.Duration),
			Timeout:         PlannerDuration(step.Timeout),
			ContinueOnError: step.ContinueOnError != nil && *step.ContinueOnError,
			Params:          copyParams(step.Params),
		})
	}

	return inputs
}

// PlannerHookReference converts a Kubernetes hook reference into planner input.
func PlannerHookReference(ref *powerv1alpha1.NamespacedNameReference) *planner.HookReference {
	if ref == nil {
		return nil
	}
	return &planner.HookReference{
		Namespace: ref.Namespace,
		Name:      ref.Name,
	}
}

// PlannerTierPolicy converts API tier policy into the pure planner shape.
func PlannerTierPolicy(policy powerv1alpha1.PowerShutdownTierPolicySpec) planner.TierPolicy {
	converted := planner.TierPolicy{
		LabelKey:      effectiveShutdownTierLabelKey(policy),
		DefaultTier:   copyInt32(policy.DefaultTier),
		Definitions:   make([]planner.TierDefinition, 0, len(policy.Tiers)),
		SelectorRules: make([]planner.TierSelector, 0, len(policy.SelectorRules)),
	}
	for _, definition := range policy.Tiers {
		converted.Definitions = append(converted.Definitions, planner.TierDefinition{
			Tier:        definition.Tier,
			Name:        definition.Name,
			Description: definition.Description,
		})
	}
	for _, rule := range policy.SelectorRules {
		converted.SelectorRules = append(converted.SelectorRules, planner.TierSelector{
			Name:    rule.Name,
			Subject: string(rule.Subject),
			Tier:    rule.Tier,
			Hash:    stableHash(rule.Selector),
		})
	}
	return converted
}

// PlannerGroupNodes resolves which real cluster nodes each shutdown group
// touches, splitting them into nodes the group acts on and nodes it powers off.
//
// This is the step that lets the planner name a node at all. Group targets are
// selectors, and the planner is pure — it receives a target summary, never the
// selector itself — so expansion happens here, against the cluster state the
// resolver already read.
//
// The two sides come from different places on purpose. Nodes a group acts on
// come from matching its node selector against real node labels. Nodes a group
// releases come from `NodePowerAgent.status.selectedNodes` by way of
// `target.agentRefs`, which is the same resolution the executor performs at
// release time — deriving it any other way would let the plan disagree with
// what execution actually does.
func PlannerGroupNodes(obj *powerv1alpha1.ShutdownFlow, bundle resolver.StructuralBundle) []planner.GroupNodeMembership {
	if obj == nil || len(obj.Spec.Groups) == 0 {
		return nil
	}
	if len(bundle.ClusterNodes) == 0 && len(bundle.AgentCoverage) == 0 {
		return nil
	}

	coverage := make(map[string][]string, len(bundle.AgentCoverage))
	for _, agent := range bundle.AgentCoverage {
		coverage[agent.Name] = agent.Nodes
	}

	membership := make([]planner.GroupNodeMembership, 0, len(obj.Spec.Groups))
	for _, group := range obj.Spec.Groups {
		entry := planner.GroupNodeMembership{Group: group.Name}
		if group.Action == powerv1alpha1.ShutdownStepAgentShutdown {
			for _, ref := range group.Target.AgentRefs {
				entry.Releases = append(entry.Releases, coverage[ref.Name]...)
			}
		} else {
			entry.Acts = matchingNodeNames(group.Target.NodeSelector, bundle.ClusterNodes)
		}
		if len(entry.Acts) == 0 && len(entry.Releases) == 0 {
			continue
		}
		membership = append(membership, entry)
	}
	if len(membership) == 0 {
		return nil
	}
	return membership
}

// matchingNodeNames returns the nodes a selector selects.
//
// A nil selector selects nothing here rather than everything. Kubernetes treats
// an empty selector as "match all", but a shutdown group with no node selector
// is not a claim about every node in the cluster — it is a group that does not
// target nodes at all, and reading it the other way would derive a clearance
// edge against every node from a group that never mentions one.
func matchingNodeNames(selector *metav1.LabelSelector, nodes []resolver.ClusterNode) []string {
	if selector == nil {
		return nil
	}
	compiled, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return nil
	}
	var matched []string
	for _, node := range nodes {
		if compiled.Matches(labels.Set(node.Labels)) {
			matched = append(matched, node.Name)
		}
	}
	return matched
}

// PlannerShutdownTier resolves a group's explicit tier or numeric tier label.
func PlannerShutdownTier(group powerv1alpha1.ShutdownGroup, policy powerv1alpha1.PowerShutdownTierPolicySpec) *int32 {
	if group.ShutdownTier != nil {
		return copyInt32(group.ShutdownTier)
	}
	if tier := shutdownTierFromSelectorRules(group.Target, policy); tier != nil {
		return tier
	}
	labelKey := effectiveShutdownTierLabelKey(policy)
	for _, selector := range []*metav1.LabelSelector{
		group.Target.NodeSelector,
		group.Target.NamespaceSelector,
		group.Target.WorkloadSelector,
	} {
		if tier := shutdownTierFromSelector(selector, labelKey); tier != nil {
			return tier
		}
	}
	return nil
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
			ShutdownTier:       step.ShutdownTier,
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
			ShutdownTier:       wave.ShutdownTier,
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
			ShutdownTier:  vertex.ShutdownTier,
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

func effectiveShutdownTierLabelKey(policy powerv1alpha1.PowerShutdownTierPolicySpec) string {
	if policy.LabelKey != "" {
		return policy.LabelKey
	}
	return powerv1alpha1.DefaultShutdownTierLabelKey
}

func shutdownTierFromSelector(selector *metav1.LabelSelector, labelKey string) *int32 {
	if selector == nil || selector.MatchLabels == nil {
		return nil
	}
	raw := selector.MatchLabels[labelKey]
	if raw == "" {
		return nil
	}
	value, err := strconv.ParseInt(raw, 10, 32)
	if err != nil {
		return nil
	}
	tier := int32(value)
	return &tier
}

func shutdownTierFromSelectorRules(target powerv1alpha1.ShutdownStepTarget, policy powerv1alpha1.PowerShutdownTierPolicySpec) *int32 {
	for _, rule := range policy.SelectorRules {
		if !tierRuleSubjectCanMatchTarget(rule.Subject, target) {
			continue
		}
		if tierRuleMatchesTarget(rule, target) {
			tier := rule.Tier
			return &tier
		}
	}
	return nil
}

func tierRuleSubjectCanMatchTarget(subject powerv1alpha1.PowerShutdownTierSubjectKind, target powerv1alpha1.ShutdownStepTarget) bool {
	switch subject {
	case powerv1alpha1.PowerShutdownTierSubjectNode:
		return target.NodeSelector != nil || len(target.AgentRefs) > 0
	case powerv1alpha1.PowerShutdownTierSubjectNamespace:
		return target.NamespaceSelector != nil || len(target.Namespaces) > 0
	case powerv1alpha1.PowerShutdownTierSubjectWorkload:
		return target.WorkloadSelector != nil || len(target.WorkloadRefs) > 0
	default:
		return target.NodeSelector != nil || target.NamespaceSelector != nil || target.WorkloadSelector != nil || len(target.AgentRefs) > 0 || len(target.Namespaces) > 0 || len(target.WorkloadRefs) > 0
	}
}

func tierRuleMatchesTarget(rule powerv1alpha1.PowerShutdownTierSelectorRule, target powerv1alpha1.ShutdownStepTarget) bool {
	for _, selector := range targetSelectorsForTierRule(rule.Subject, target) {
		if labelSelectorMatchesTargetSelector(rule.Selector, selector) {
			return true
		}
	}
	return false
}

func targetSelectorsForTierRule(subject powerv1alpha1.PowerShutdownTierSubjectKind, target powerv1alpha1.ShutdownStepTarget) []*metav1.LabelSelector {
	switch subject {
	case powerv1alpha1.PowerShutdownTierSubjectNode:
		return []*metav1.LabelSelector{target.NodeSelector}
	case powerv1alpha1.PowerShutdownTierSubjectNamespace:
		return []*metav1.LabelSelector{target.NamespaceSelector}
	case powerv1alpha1.PowerShutdownTierSubjectWorkload:
		return []*metav1.LabelSelector{target.WorkloadSelector}
	default:
		return []*metav1.LabelSelector{target.NodeSelector, target.NamespaceSelector, target.WorkloadSelector}
	}
}

func labelSelectorMatchesTargetSelector(ruleSelector metav1.LabelSelector, targetSelector *metav1.LabelSelector) bool {
	if targetSelector == nil {
		return false
	}
	selector, err := metav1.LabelSelectorAsSelector(&ruleSelector)
	if err != nil {
		return false
	}
	return selector.Matches(labels.Set(targetSelector.MatchLabels))
}

func copyInt32(value *int32) *int32 {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}

func stableHash(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(fmt.Sprintf("shutdownflow adapter value could not be encoded for hashing: %v", err))
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}
