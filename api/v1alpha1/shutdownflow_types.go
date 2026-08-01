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

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
)

// ShutdownFlowSpec defines the desired state of ShutdownFlow
type ShutdownFlowSpec struct {
	// managementClusterRef references the owning PowerManagementCluster.
	// +optional
	ManagementClusterRef *ObjectNameReference `json:"managementClusterRef,omitempty"`

	// mode controls whether the flow only compiles/evaluates or may execute actions.
	// +kubebuilder:default=DryRun
	// +optional
	Mode ShutdownFlowMode `json:"mode,omitempty"`

	// triggers define when this flow becomes eligible.
	// +kubebuilder:validation:MinItems=1
	Triggers []ShutdownTrigger `json:"triggers"`

	// groups define the dependency graph used to compile ordered shutdown waves.
	// +optional
	Groups []ShutdownGroup `json:"groups,omitempty"`

	// steps define a linear fallback plan for simple installs.
	// +optional
	Steps []ShutdownStep `json:"steps,omitempty"`

	// concurrencyPolicy controls overlapping flow evaluations.
	// +kubebuilder:validation:Enum=Forbid;Replace
	// +kubebuilder:default=Forbid
	// +optional
	ConcurrencyPolicy string `json:"concurrencyPolicy,omitempty"`

	// abortPolicy defines behavior after a failed or unsafe step.
	// +optional
	AbortPolicy AbortPolicySpec `json:"abortPolicy,omitempty"`

	// safety defines global safety gates for this flow.
	// +optional
	Safety FlowSafetySpec `json:"safety,omitempty"`
}

// ShutdownFlowStatus defines the observed state of ShutdownFlow.
type ShutdownFlowStatus struct {
	// observedGeneration is the last generation reconciled by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// phase summarizes the current flow state.
	// +optional
	Phase ShutdownFlowPhase `json:"phase,omitempty"`

	// compiledSteps is the controller-compiled view of the flow for pre-flight review.
	// +optional
	CompiledSteps []CompiledShutdownStep `json:"compiledSteps,omitempty"`

	// compiledWaves is the dependency-graph execution plan grouped into ordered waves.
	// +optional
	CompiledWaves []CompiledShutdownWave `json:"compiledWaves,omitempty"`

	// estimatedDuration is the cumulative expected duration of the compiled plan.
	// +optional
	EstimatedDuration *metav1.Duration `json:"estimatedDuration,omitempty"`

	// configHash identifies the compiled plan.
	// +optional
	ConfigHash string `json:"configHash,omitempty"`

	// resolvedInputHash identifies the declarative inventory and UPS capability inputs used by the compiled plan.
	// +optional
	ResolvedInputHash string `json:"resolvedInputHash,omitempty"`

	// topologyHash identifies the compiled declarative power and communication topology.
	// +optional
	TopologyHash string `json:"topologyHash,omitempty"`

	// inventoryEntityCount records how many declarative inventory entities fed this plan.
	// +optional
	InventoryEntityCount int32 `json:"inventoryEntityCount,omitempty"`

	// inventoryEdgeCount records how many declarative inventory edges fed this plan.
	// +optional
	InventoryEdgeCount int32 `json:"inventoryEdgeCount,omitempty"`

	// capabilityMatchCount records how many UPS devices received capability profile matches.
	// +optional
	CapabilityMatchCount int32 `json:"capabilityMatchCount,omitempty"`

	// lastEvaluationTime is the most recent policy evaluation.
	// +optional
	LastEvaluationTime *metav1.Time `json:"lastEvaluationTime,omitempty"`

	// conditions represent the current state of the ShutdownFlow resource.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// ShutdownFlowMode controls action authority.
// +kubebuilder:validation:Enum=DryRun;Enforce
type ShutdownFlowMode string

const (
	ShutdownFlowModeDryRun  ShutdownFlowMode = "DryRun"
	ShutdownFlowModeEnforce ShutdownFlowMode = "Enforce"
)

// ShutdownTriggerType defines flow trigger semantics.
// +kubebuilder:validation:Enum=OnBattery;LowBattery;RuntimeBelow;ChargeBelow;TelemetryStale
type ShutdownTriggerType string

const (
	ShutdownTriggerOnBattery      ShutdownTriggerType = "OnBattery"
	ShutdownTriggerLowBattery     ShutdownTriggerType = "LowBattery"
	ShutdownTriggerRuntimeBelow   ShutdownTriggerType = "RuntimeBelow"
	ShutdownTriggerChargeBelow    ShutdownTriggerType = "ChargeBelow"
	ShutdownTriggerTelemetryStale ShutdownTriggerType = "TelemetryStale"
)

// ShutdownTrigger defines when a flow is eligible to run.
type ShutdownTrigger struct {
	// type is the trigger kind.
	Type ShutdownTriggerType `json:"type"`

	// upsDeviceRefs limits this trigger to specific UPSDevice resources.
	// +optional
	UPSDeviceRefs []ObjectNameReference `json:"upsDeviceRefs,omitempty"`

	// powerDomains limits this trigger to named power domains.
	// +optional
	PowerDomains []string `json:"powerDomains,omitempty"`

	// for requires the trigger condition to hold for this duration.
	// +optional
	For *metav1.Duration `json:"for,omitempty"`

	// runtimeBelowSeconds is used by RuntimeBelow triggers.
	// +kubebuilder:validation:Minimum=0
	// +optional
	RuntimeBelowSeconds *int64 `json:"runtimeBelowSeconds,omitempty"`

	// chargeBelowPercent is used by ChargeBelow triggers.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	// +optional
	ChargeBelowPercent *int32 `json:"chargeBelowPercent,omitempty"`
}

// ShutdownGroup defines a shutdown subject and its graph relationships.
type ShutdownGroup struct {
	// name is unique within the flow and is referenced by dependency fields.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// description explains the operator-facing purpose of the group.
	// +optional
	Description string `json:"description,omitempty"`

	// target selects the resources affected by this group.
	// +optional
	Target ShutdownStepTarget `json:"target,omitempty"`

	// action selects the operation applied to the selected target.
	Action ShutdownStepType `json:"action"`

	// requires names groups that must remain available while this group shuts down.
	// During shutdown this group is ordered before the groups it requires.
	// +optional
	Requires []string `json:"requires,omitempty"`

	// before names groups that cannot begin until this group has completed.
	// +optional
	Before []string `json:"before,omitempty"`

	// after names groups that must complete before this group can begin.
	// +optional
	After []string `json:"after,omitempty"`

	// phase is a numeric fallback ordering hint. Lower phases are compiled earlier.
	// Explicit dependencies still take precedence.
	// +optional
	Phase *int32 `json:"phase,omitempty"`

	// timeout limits how long the group may run.
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// params holds action-specific scalar parameters.
	// +optional
	Params map[string]string `json:"params,omitempty"`
}

// ShutdownStepType defines supported flow actions.
// +kubebuilder:validation:Enum=Notify;Wait;Gate;CordonNodes;DrainNodes;ScaleWorkload;RunWorkflow;AgentShutdown
type ShutdownStepType string

const (
	ShutdownStepNotify        ShutdownStepType = "Notify"
	ShutdownStepWait          ShutdownStepType = "Wait"
	ShutdownStepGate          ShutdownStepType = "Gate"
	ShutdownStepCordonNodes   ShutdownStepType = "CordonNodes"
	ShutdownStepDrainNodes    ShutdownStepType = "DrainNodes"
	ShutdownStepScaleWorkload ShutdownStepType = "ScaleWorkload"
	ShutdownStepRunWorkflow   ShutdownStepType = "RunWorkflow"
	ShutdownStepAgentShutdown ShutdownStepType = "AgentShutdown"
)

// ShutdownStep defines one ordered flow action.
type ShutdownStep struct {
	// id is unique within the flow.
	// +kubebuilder:validation:MinLength=1
	ID string `json:"id"`

	// type selects the action implementation.
	Type ShutdownStepType `json:"type"`

	// description explains the operator-facing purpose of the step.
	// +optional
	Description string `json:"description,omitempty"`

	// target selects nodes, namespaces, or workloads depending on step type.
	// +optional
	Target ShutdownStepTarget `json:"target,omitempty"`

	// duration is used by Wait steps.
	// +optional
	Duration *metav1.Duration `json:"duration,omitempty"`

	// timeout limits how long the step may run.
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// continueOnError allows later steps to continue after this step fails.
	// +kubebuilder:default=false
	// +optional
	ContinueOnError *bool `json:"continueOnError,omitempty"`

	// params holds step-type-specific scalar parameters.
	// +optional
	Params map[string]string `json:"params,omitempty"`
}

// ShutdownStepTarget selects objects affected by a flow step.
type ShutdownStepTarget struct {
	// nodeSelector selects Nodes for node-oriented steps.
	// +optional
	NodeSelector *metav1.LabelSelector `json:"nodeSelector,omitempty"`

	// namespaceSelector selects namespaces for namespace-oriented steps.
	// +optional
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`

	// namespaces selects namespaces for namespace-oriented steps.
	// +optional
	Namespaces []string `json:"namespaces,omitempty"`

	// workloadSelector selects workloads by labels within selected namespaces.
	// +optional
	WorkloadSelector *metav1.LabelSelector `json:"workloadSelector,omitempty"`

	// workloadRefs selects specific workloads.
	// +optional
	WorkloadRefs []WorkloadReference `json:"workloadRefs,omitempty"`

	// agentRefs selects NodePowerAgent fleets for AgentShutdown steps.
	// +optional
	AgentRefs []ObjectNameReference `json:"agentRefs,omitempty"`
}

// WorkloadReference identifies a namespaced Kubernetes workload.
type WorkloadReference struct {
	// apiVersion is the workload apiVersion.
	// +kubebuilder:validation:MinLength=1
	APIVersion string `json:"apiVersion"`

	// kind is the workload kind.
	// +kubebuilder:validation:MinLength=1
	Kind string `json:"kind"`

	// namespace is the workload namespace.
	// +kubebuilder:validation:MinLength=1
	Namespace string `json:"namespace"`

	// name is the workload name.
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// AbortBehavior defines flow abort behavior.
// +kubebuilder:validation:Enum=HaltAndSurface;ContinueSafeSteps
type AbortBehavior string

const (
	AbortBehaviorHaltAndSurface    AbortBehavior = "HaltAndSurface"
	AbortBehaviorContinueSafeSteps AbortBehavior = "ContinueSafeSteps"
)

// AbortPolicySpec defines behavior after a failed or unsafe step.
type AbortPolicySpec struct {
	// behavior controls what happens after abort.
	// +kubebuilder:default=HaltAndSurface
	// +optional
	Behavior AbortBehavior `json:"behavior,omitempty"`

	// notify enables notification steps after abort.
	// +kubebuilder:default=true
	// +optional
	Notify *bool `json:"notify,omitempty"`
}

// FlowSafetySpec defines global flow safety gates.
type FlowSafetySpec struct {
	// requireManualApproval requires an approval annotation before Enforce mode can execute.
	// +kubebuilder:default=true
	// +optional
	RequireManualApproval *bool `json:"requireManualApproval,omitempty"`

	// approvalAnnotation is the annotation key accepted as manual approval.
	// +optional
	ApprovalAnnotation string `json:"approvalAnnotation,omitempty"`

	// maxEstimatedDuration rejects compiled plans that exceed this budget.
	// +optional
	MaxEstimatedDuration *metav1.Duration `json:"maxEstimatedDuration,omitempty"`
}

// CompiledShutdownStep is the status-visible compiled form of one flow step.
type CompiledShutdownStep struct {
	// id is the source step id.
	ID string `json:"id"`

	// index is the zero-based compiled order.
	Index int32 `json:"index"`

	// type is the source step type.
	Type ShutdownStepType `json:"type"`

	// targetSummary summarizes selected targets without storing bulky object lists.
	// +optional
	TargetSummary string `json:"targetSummary,omitempty"`

	// cumulativeDuration is the expected elapsed time through this step.
	// +optional
	CumulativeDuration *metav1.Duration `json:"cumulativeDuration,omitempty"`
}

// CompiledShutdownWave is the status-visible execution form of shutdown groups.
type CompiledShutdownWave struct {
	// index is the zero-based wave order.
	Index int32 `json:"index"`

	// phase is the phase hint shared by this wave, when one was present.
	// +optional
	Phase *int32 `json:"phase,omitempty"`

	// groups are the shutdown group names in this wave.
	Groups []string `json:"groups"`

	// duration is the maximum expected duration of any group in this wave.
	// +optional
	Duration *metav1.Duration `json:"duration,omitempty"`

	// cumulativeDuration is the expected elapsed time through this wave.
	// +optional
	CumulativeDuration *metav1.Duration `json:"cumulativeDuration,omitempty"`
}

// ShutdownFlowPhase summarizes flow compilation and evaluation.
// +kubebuilder:validation:Enum=Pending;Compiled;Blocked;Running;Aborted;Completed;Error
type ShutdownFlowPhase string

const (
	ShutdownFlowPhasePending   ShutdownFlowPhase = "Pending"
	ShutdownFlowPhaseCompiled  ShutdownFlowPhase = "Compiled"
	ShutdownFlowPhaseBlocked   ShutdownFlowPhase = "Blocked"
	ShutdownFlowPhaseRunning   ShutdownFlowPhase = "Running"
	ShutdownFlowPhaseAborted   ShutdownFlowPhase = "Aborted"
	ShutdownFlowPhaseCompleted ShutdownFlowPhase = "Completed"
	ShutdownFlowPhaseError     ShutdownFlowPhase = "Error"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// ShutdownFlow is the Schema for the shutdownflows API
type ShutdownFlow struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitzero"`

	// spec defines the desired state of ShutdownFlow
	// +required
	Spec ShutdownFlowSpec `json:"spec"`

	// status defines the observed state of ShutdownFlow
	// +optional
	Status ShutdownFlowStatus `json:"status,omitzero"`
}

// +kubebuilder:object:root=true

// ShutdownFlowList contains a list of ShutdownFlow
type ShutdownFlowList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitzero"`
	Items           []ShutdownFlow `json:"items"`
}

func init() {
	SchemeBuilder.Register(func(s *runtime.Scheme) error {
		s.AddKnownTypes(SchemeGroupVersion, &ShutdownFlow{}, &ShutdownFlowList{})
		return nil
	})
}
