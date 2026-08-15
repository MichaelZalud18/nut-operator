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
	corev1 "k8s.io/api/core/v1"
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

	// tierOverrunPolicy controls what the executor does when a tier reaches its
	// compiled duration while a lower tier is waiting behind it.
	// +kubebuilder:validation:Enum=Wait;Overlap;Preempt
	// +kubebuilder:default=Wait
	// +optional
	TierOverrunPolicy ShutdownTierOverrunPolicy `json:"tierOverrunPolicy,omitempty"`

	// rehearsal controls how explicitly requested rehearsal executions contribute
	// to observed-duration estimates. Rehearsals are requested by changing the
	// power.zalud.io/rehearsal-request annotation to a new non-empty token.
	// +optional
	Rehearsal ShutdownRehearsalSpec `json:"rehearsal,omitempty"`

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

	// publishedArtifact is the structured planner artifact published for subscribers.
	// +optional
	PublishedArtifact *PublishedPlannerArtifactStatus `json:"publishedArtifact,omitempty"`

	// blockedNodeReleases names nodes this flow will not power off because a group still
	// expected to be running is scheduled on them (OD-18). They are published because a
	// flow that quietly shuts down fewer nodes than its plan implies is worse than one
	// that says which nodes it is leaving up and why.
	// +listType=map
	// +listMapKey=nodeName
	// +optional
	BlockedNodeReleases []BlockedNodeReleaseStatus `json:"blockedNodeReleases,omitempty"`

	// estimatedDuration is the cumulative expected duration of the compiled plan.
	// +optional
	EstimatedDuration *metav1.Duration `json:"estimatedDuration,omitempty"`

	// planFeasibility compares that estimate against the runtime the UPS devices report
	// (OD-12, EX-32). It warns; it never blocks. The flow author holds the risk, and this
	// operator owes them the numbers rather than a decision made on their behalf.
	// +optional
	PlanFeasibility *PlanFeasibilityStatus `json:"planFeasibility,omitempty"`

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

	// lastPublishTime is when this flow's state was last republished, whether or not
	// anything changed (AE-5). It is the heartbeat that lets a subscriber tell
	// "nothing is happening" from "the publisher died" — without it both look like
	// silence. Refreshed on a cadence that is faster while a flow is active.
	// +optional
	LastPublishTime *metav1.Time `json:"lastPublishTime,omitempty"`

	// triggerEvaluation is the most recent trigger evaluation against UPS telemetry status.
	// +optional
	TriggerEvaluation *ShutdownTriggerEvaluationStatus `json:"triggerEvaluation,omitempty"`

	// triggerHoldStates persists compact hold timers for triggers with spec.triggers[].for.
	// +optional
	TriggerHoldStates []ShutdownTriggerHoldStateStatus `json:"triggerHoldStates,omitempty"`

	// lastExecution summarizes the most recent executor handoff and stores the active-trigger dedupe key.
	// +optional
	LastExecution *ShutdownExecutionStatus `json:"lastExecution,omitempty"`

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

// ShutdownTierOverrunPolicy controls how a flow handles a tier that runs past
// the time allocated to it while a lower tier is waiting.
//
// Wait is conservative and lets the tier finish. Overlap starts the next tier on
// schedule while the previous tier continues. Preempt cancels the tier's action
// context when its budget expires, so actions that honor context stop and the
// lower tier may start.
// +kubebuilder:validation:Enum=Wait;Overlap;Preempt
type ShutdownTierOverrunPolicy string

const (
	// ShutdownTierOverrunWait starts the next tier only after the current tier finishes.
	ShutdownTierOverrunWait ShutdownTierOverrunPolicy = "Wait"
	// ShutdownTierOverrunOverlap starts the next tier on schedule while the current tier continues.
	ShutdownTierOverrunOverlap ShutdownTierOverrunPolicy = "Overlap"
	// ShutdownTierOverrunPreempt cancels the current tier when the next tier becomes due.
	ShutdownTierOverrunPreempt ShutdownTierOverrunPolicy = "Preempt"
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

// Value dereferences an optional trigger type, yielding "" when unset. Safe on a nil receiver so
// callers can read spec.triggers[].fallbackType without a guard at every use.
func (t *ShutdownTriggerType) Value() string {
	if t == nil {
		return ""
	}
	return string(*t)
}

// ShutdownTrigger defines when a flow is eligible to run.
type ShutdownTrigger struct {
	// type is the trigger kind.
	Type ShutdownTriggerType `json:"type"`

	// fallbackType is the coarser trigger class to evaluate for devices that cannot report the
	// telemetry this trigger needs (OD-9).
	//
	// Substitution is declared rather than automatic. It changes when a shutdown starts -- a
	// RuntimeBelow trigger acts on a threshold the author chose against a runtime budget, while its
	// LowBattery fallback acts whenever the device itself says the battery is low -- and per GP-5
	// anything that alters failure-path behavior is declared ahead of time, not derived during the
	// outage. Compilation names the fallback that would cover an uncovered device, so adopting one
	// is a mechanical edit rather than a research task.
	//
	// Only a strictly coarser class is accepted: RuntimeBelow and ChargeBelow may fall back to
	// LowBattery. OnBattery and LowBattery already need only ups.status and have nothing coarser to
	// fall back to; TelemetryStale needs no telemetry at all.
	// +optional
	FallbackType *ShutdownTriggerType `json:"fallbackType,omitempty"`

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

// ShutdownTriggerEvaluationStatus summarizes one trigger evaluation tick.
type ShutdownTriggerEvaluationStatus struct {
	// observedAt is the controller-supplied time used for the evaluation.
	// +optional
	ObservedAt *metav1.Time `json:"observedAt,omitempty"`

	// mode is the flow mode at evaluation time.
	// +optional
	Mode ShutdownFlowMode `json:"mode,omitempty"`

	// eligible is true when at least one trigger is eligible to proceed to the executor boundary.
	Eligible bool `json:"eligible"`

	// reason is the high-level evaluation reason.
	// +optional
	Reason string `json:"reason,omitempty"`

	// matchedTriggerCount records how many triggers matched telemetry before hold-time gating.
	// +optional
	MatchedTriggerCount int32 `json:"matchedTriggerCount,omitempty"`

	// selectedUPSDevices is the sorted union of UPS devices selected by eligible triggers.
	// +optional
	SelectedUPSDevices []string `json:"selectedUPSDevices,omitempty"`

	// planConfigHash is the compiled plan hash evaluated with this trigger tick.
	// +optional
	PlanConfigHash string `json:"planConfigHash,omitempty"`

	// decisions records one decision per configured trigger.
	// +optional
	Decisions []ShutdownTriggerDecisionStatus `json:"decisions,omitempty"`

	// diagnostics records non-fatal warnings or trigger definition errors surfaced by evaluation.
	// +optional
	Diagnostics []ShutdownTriggerDiagnosticStatus `json:"diagnostics,omitempty"`
}

// ShutdownTriggerDecisionStatus records the evaluation result for one trigger.
type ShutdownTriggerDecisionStatus struct {
	// triggerID is the controller-generated stable identifier for this trigger position.
	// +optional
	TriggerID string `json:"triggerID,omitempty"`

	// type is the trigger kind.
	// +optional
	Type ShutdownTriggerType `json:"type,omitempty"`

	// matched is true when the telemetry condition is currently true before hold-time gating.
	Matched bool `json:"matched"`

	// eligible is true when the telemetry condition is true and hold-time gating has elapsed.
	Eligible bool `json:"eligible"`

	// reason explains the decision.
	// +optional
	Reason string `json:"reason,omitempty"`

	// selectedUPSDevices names UPS devices matched by this trigger.
	// +optional
	SelectedUPSDevices []string `json:"selectedUPSDevices,omitempty"`

	// holdStartedAt records when this trigger/device condition first became true.
	// +optional
	HoldStartedAt *metav1.Time `json:"holdStartedAt,omitempty"`

	// eligibleAt records when the hold duration is satisfied.
	// +optional
	EligibleAt *metav1.Time `json:"eligibleAt,omitempty"`
}

// ShutdownTriggerHoldStateStatus persists one active trigger hold timer.
type ShutdownTriggerHoldStateStatus struct {
	// triggerID identifies the trigger whose condition is being held.
	TriggerID string `json:"triggerID"`

	// upsDevice is the UPSDevice name whose condition is being held.
	UPSDevice string `json:"upsDevice"`

	// startedAt records when the condition first became true.
	StartedAt metav1.Time `json:"startedAt"`
}

// ShutdownTriggerDiagnosticStatus records a trigger-evaluation warning or error.
type ShutdownTriggerDiagnosticStatus struct {
	// severity is Warning or Error.
	Severity string `json:"severity"`

	// reason is a machine-readable diagnostic reason.
	Reason string `json:"reason"`

	// subject identifies the affected trigger or UPS device.
	// +optional
	Subject string `json:"subject,omitempty"`

	// message is the human-readable diagnostic.
	Message string `json:"message"`
}

// ShutdownExecutionStatus summarizes the most recent executor run for a flow.
type ShutdownExecutionStatus struct {
	// executionID identifies the durable execution record.
	// +optional
	ExecutionID string `json:"executionID,omitempty"`

	// deduplicationKey identifies the active trigger episode and compiled plan this execution covers.
	// +optional
	DeduplicationKey string `json:"deduplicationKey,omitempty"`

	// triggerActive is true while the trigger episode represented by deduplicationKey remains eligible.
	TriggerActive bool `json:"triggerActive"`

	// phase summarizes the executor result.
	// +optional
	Phase ShutdownExecutionPhase `json:"phase,omitempty"`

	// mode is the flow mode observed at execution time.
	// +optional
	Mode ShutdownFlowMode `json:"mode,omitempty"`

	// dryRun is true when the executor suppressed all effectful actions.
	DryRun bool `json:"dryRun"`

	// rehearsal is true when this execution was explicitly requested as a rehearsal
	// rather than started by an eligible power trigger.
	// +optional
	Rehearsal bool `json:"rehearsal,omitempty"`

	// planConfigHash is the compiled plan hash executed.
	// +optional
	PlanConfigHash string `json:"planConfigHash,omitempty"`

	// selectedUPSDevices is the sorted union of UPS devices that made the trigger eligible.
	// +optional
	SelectedUPSDevices []string `json:"selectedUPSDevices,omitempty"`

	// startedAt records when execution evidence recording began.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// completedAt records when execution evidence recording finished.
	// +optional
	CompletedAt *metav1.Time `json:"completedAt,omitempty"`

	// waveCount records how many compiled waves were handed to the executor.
	// +optional
	WaveCount int32 `json:"waveCount,omitempty"`

	// groupCount records how many groups produced execution evidence.
	// +optional
	GroupCount int32 `json:"groupCount,omitempty"`

	// actionAttemptCount records how many action attempts were recorded.
	// +optional
	ActionAttemptCount int32 `json:"actionAttemptCount,omitempty"`

	// nodeReleaseCount records how many node release decisions were recorded.
	// +optional
	NodeReleaseCount int32 `json:"nodeReleaseCount,omitempty"`

	// reason explains why the execution was started or skipped.
	// +optional
	Reason string `json:"reason,omitempty"`

	// message is a human-readable execution summary.
	// +optional
	Message string `json:"message,omitempty"`

	// adaptive is the tier pointer and timing mode this execution ended on.
	// +optional
	Adaptive *ShutdownExecutionAdaptiveStatus `json:"adaptive,omitempty"`

	// tierOverruns records tier budget overruns observed during this execution.
	// The values are facts: which tier overran, the budget it had, how long it
	// actually took, and what configured policy action was taken.
	// +optional
	TierOverruns []ShutdownTierOverrunStatus `json:"tierOverruns,omitempty"`
}

// ShutdownTierOverrunStatus records one tier transition whose elapsed time
// exceeded the tier budget derived from the compiled plan.
type ShutdownTierOverrunStatus struct {
	// waveIndex is the compiled wave after which the lower tier became due.
	WaveIndex int32 `json:"waveIndex"`

	// shutdownTier is the tier that overran.
	// +optional
	ShutdownTier *int32 `json:"shutdownTier,omitempty"`

	// policy is the configured spec.tierOverrunPolicy used for this overrun.
	// +optional
	Policy ShutdownTierOverrunPolicy `json:"policy,omitempty"`

	// action names what the executor did because of that policy.
	// +optional
	Action string `json:"action,omitempty"`

	// declaredSeconds is the tier's uncompressed declared duration, rounded up to
	// a whole second.
	// +optional
	DeclaredSeconds int64 `json:"declaredSeconds,omitempty"`

	// effectiveSeconds is the tier budget after adaptive timing compression,
	// rounded up to a whole second.
	// +optional
	EffectiveSeconds int64 `json:"effectiveSeconds,omitempty"`

	// actualSeconds is how long the tier actually ran, rounded up to a whole second.
	// +optional
	ActualSeconds int64 `json:"actualSeconds,omitempty"`

	// overrunSeconds is actualSeconds minus effectiveSeconds, rounded up to a whole second.
	// +optional
	OverrunSeconds int64 `json:"overrunSeconds,omitempty"`
}

// ShutdownRehearsalSpec controls deliberately requested execution history.
type ShutdownRehearsalSpec struct {
	// includeInEstimates controls whether non-dry-run rehearsal executions are
	// folded into EX-32 observed-duration estimates. Defaults true: a rehearsal is
	// real work against real targets, so its durations are evidence unless the
	// flow author opts out.
	// +kubebuilder:default=true
	// +optional
	IncludeInEstimates *bool `json:"includeInEstimates,omitempty"`
}

// ShutdownExecutionAdaptiveStatus publishes where a flow progressed to and how
// much time it was allowing when it got there.
//
// Every field is a fact about current or recorded state, which is the AE-4 line:
// the tier reached, the mode in force, the power observed. Nothing here
// characterizes the sequence as a dip, a flicker, or a recovery, and nothing
// projects where power is heading — those are interpretations a subscriber owns.
type ShutdownExecutionAdaptiveStatus struct {
	// tier is the shutdown tier the pointer currently occupies.
	// +optional
	Tier int32 `json:"tier,omitempty"`

	// deepestTier is the lowest-numbered tier this flow ever reached. It differs from
	// tier after power recovered and the pointer ascended, and it is what makes a
	// re-descent recognizable as re-execution rather than new work.
	// +optional
	DeepestTier int32 `json:"deepestTier,omitempty"`

	// pointerStarted distinguishes a flow waiting at a tier from one that has
	// executed it.
	// +optional
	PointerStarted bool `json:"pointerStarted,omitempty"`

	// timingMode is the timing mode in force: Relaxed, Nominal, or Urgent.
	// +kubebuilder:validation:Enum=Relaxed;Nominal;Urgent
	// +optional
	TimingMode string `json:"timingMode,omitempty"`

	// onBattery and lowBattery are the power state observed at the last wave
	// boundary.
	// +optional
	OnBattery bool `json:"onBattery,omitempty"`
	// +optional
	LowBattery bool `json:"lowBattery,omitempty"`

	// runtimeSeconds is the shortest remaining runtime across the selected devices.
	// Absent means no trustworthy figure was available, which is deliberately
	// distinct from zero seconds remaining.
	// +optional
	RuntimeSeconds *int64 `json:"runtimeSeconds,omitempty"`

	// runtimeTrusted records whether the devices affirmatively declared a dynamic
	// runtime estimate (AE-6). False means the runtime figure did not drive timing.
	// +optional
	RuntimeTrusted bool `json:"runtimeTrusted,omitempty"`

	// events are the state transitions recorded during this execution, in order.
	// +optional
	Events []string `json:"events,omitempty"`
}

// PlanFeasibilityStatus is the published OD-12 warning surface.
//
// Facts and a comparison of them, per EX-28. There is no verdict adjective, no
// probability, and no projected completion time — those need a theory about what
// the history means, which belongs to whoever is reading this.
type PlanFeasibilityStatus struct {
	// planSeconds is the compiled plan's estimated duration.
	PlanSeconds int64 `json:"planSeconds"`

	// runtimeSeconds is the shortest runtime reported across the selected devices, when
	// one is known and trusted (CR-4). Absent means unknown, which is never read as
	// headroom.
	// +optional
	RuntimeSeconds *int64 `json:"runtimeSeconds,omitempty"`

	// fits is true only when a runtime is known and the plan estimate is within it.
	// Unknown runtime yields false, matching PL-32: missing data never produces an
	// optimistic verdict.
	Fits bool `json:"fits"`

	// observedGroups and declaredGroups say how much of the estimate came from measured
	// executions rather than from declared timeouts.
	ObservedGroups int32 `json:"observedGroups"`
	DeclaredGroups int32 `json:"declaredGroups"`

	// thinGroups names groups whose estimate rests on fewer than two observations. These
	// are what a rehearsal would improve (EX-33).
	// +optional
	ThinGroups []string `json:"thinGroups,omitempty"`

	// message states the comparison in the form an operator reads first.
	// +optional
	Message string `json:"message,omitempty"`
}

// ShutdownExecutionPhase summarizes executor progress.
// +kubebuilder:validation:Enum=Running;Completed;Aborted;Failed;Skipped
type ShutdownExecutionPhase string

const (
	ShutdownExecutionPhaseRunning   ShutdownExecutionPhase = "Running"
	ShutdownExecutionPhaseCompleted ShutdownExecutionPhase = "Completed"
	ShutdownExecutionPhaseAborted   ShutdownExecutionPhase = "Aborted"
	ShutdownExecutionPhaseFailed    ShutdownExecutionPhase = "Failed"
	ShutdownExecutionPhaseSkipped   ShutdownExecutionPhase = "Skipped"
)

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

	// hookRef references the ShutdownHook invoked by RunHook.
	// +optional
	HookRef *NamespacedNameReference `json:"hookRef,omitempty"`

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

	// shutdownTier assigns this group to a numbered shutdown tier. Higher tiers stop earlier; tier 0 cannot be targeted.
	// +kubebuilder:validation:Minimum=0
	// +optional
	ShutdownTier *int32 `json:"shutdownTier,omitempty"`

	// tierInversionPolicy decides what happens when this group runs on a node whose own
	// shutdown tier would power that node off while this group is still expected to be
	// running. Block withholds the node from power-off for the whole flow, so the flow
	// shuts down less than planned rather than pulling power from live work. Allow powers
	// the node off on schedule and accepts that this group goes down with it.
	// +kubebuilder:validation:Enum=Block;Allow
	// +kubebuilder:default=Block
	// +optional
	TierInversionPolicy ShutdownTierInversionPolicy `json:"tierInversionPolicy,omitempty"`

	// timeout limits how long the group may run.
	// +optional
	Timeout *metav1.Duration `json:"timeout,omitempty"`

	// params holds action-specific scalar parameters.
	// +optional
	Params map[string]string `json:"params,omitempty"`
}

// BlockedNodeReleaseStatus is one node the flow will not power off, and the
// group whose tier caused it to be held back.
type BlockedNodeReleaseStatus struct {
	// nodeName is the cluster node withheld from power-off.
	// +kubebuilder:validation:MinLength=1
	NodeName string `json:"nodeName"`

	// reason is the machine-readable cause, currently always ShutdownTierInversion.
	// +optional
	Reason string `json:"reason,omitempty"`

	// groups names the shutdown groups running on this node that are scheduled to
	// outlive it.
	// +optional
	Groups []string `json:"groups,omitempty"`

	// nodeTier is the node's own declared shutdown tier.
	// +optional
	NodeTier int32 `json:"nodeTier,omitempty"`

	// message explains the block in operator-facing terms.
	// +optional
	Message string `json:"message,omitempty"`
}

// ShutdownTierInversionPolicy selects the remedy for a tier inversion: a group
// scheduled to keep running on a node scheduled to power off first (OD-18).
//
// Blocking is the default because its failure mode is losing less of the cluster
// than intended, while allowing the power-off risks cutting power to work that
// was explicitly declared as still needed — and node-local storage means the
// third option, migrating the workload elsewhere, is not always available.
// +kubebuilder:validation:Enum=Block;Allow
type ShutdownTierInversionPolicy string

const (
	// ShutdownTierInversionBlock withholds the inverted node from power-off.
	ShutdownTierInversionBlock ShutdownTierInversionPolicy = "Block"
	// ShutdownTierInversionAllow powers the inverted node off on schedule.
	ShutdownTierInversionAllow ShutdownTierInversionPolicy = "Allow"
)

// ShutdownStepType defines supported flow actions.
// +kubebuilder:validation:Enum=Notify;Wait;CordonNodes;DrainNodes;ScaleWorkload;RunHook;AgentShutdown
type ShutdownStepType string

const (
	ShutdownStepNotify        ShutdownStepType = "Notify"
	ShutdownStepWait          ShutdownStepType = "Wait"
	ShutdownStepCordonNodes   ShutdownStepType = "CordonNodes"
	ShutdownStepDrainNodes    ShutdownStepType = "DrainNodes"
	ShutdownStepScaleWorkload ShutdownStepType = "ScaleWorkload"
	ShutdownStepRunHook       ShutdownStepType = "RunHook"
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

	// hookRef references the ShutdownHook invoked by RunHook.
	// +optional
	HookRef *NamespacedNameReference `json:"hookRef,omitempty"`

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

	// nodeSelectorRequirements further constrain Nodes for node-oriented steps
	// using Kubernetes node selector operators, including Gt and Lt.
	// Requirements are ANDed with nodeSelector.
	// +optional
	NodeSelectorRequirements []corev1.NodeSelectorRequirement `json:"nodeSelectorRequirements,omitempty"`

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

	// allowUnidentifiedDevices permits Enforce mode when a UPS in scope matched
	// no product capability profile and fell through to the unidentified-device
	// profile.
	//
	// Defaults to false, and that default is the point. An unidentified device
	// has had nothing verified about it: the operator knows only that some NUT
	// driver answered. UPS hardware varies too much for that to be a safe basis
	// for cutting power to real nodes, so enforcement is blocked until either a
	// profile matches or an operator states, in Git, that they accept it.
	// +kubebuilder:default=false
	// +optional
	AllowUnidentifiedDevices *bool `json:"allowUnidentifiedDevices,omitempty"`
}

// CompiledShutdownStep is the status-visible compiled form of one flow step.
type CompiledShutdownStep struct {
	// id is the source step id.
	ID string `json:"id"`

	// index is the zero-based compiled order.
	Index int32 `json:"index"`

	// type is the source step type.
	Type ShutdownStepType `json:"type"`

	// shutdownTier is the effective numbered shutdown tier, when present.
	// +optional
	ShutdownTier *int32 `json:"shutdownTier,omitempty"`

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

	// shutdownTier is the numbered tier shared by this wave, when tier policy assigned one.
	// +optional
	ShutdownTier *int32 `json:"shutdownTier,omitempty"`

	// groups are the shutdown group names in this wave.
	Groups []string `json:"groups"`

	// duration is the maximum expected duration of any group in this wave.
	// +optional
	Duration *metav1.Duration `json:"duration,omitempty"`

	// cumulativeDuration is the expected elapsed time through this wave.
	// +optional
	CumulativeDuration *metav1.Duration `json:"cumulativeDuration,omitempty"`
}

// PublishedPlannerArtifactStatus is the compact Kubernetes status view of the planner artifact.
type PublishedPlannerArtifactStatus struct {
	// graph is the normalized dependency graph.
	// +optional
	Graph PlannerGraphStatus `json:"graph,omitempty"`

	// powerDomains are the resolver-derived power-domain closures available to subscribers.
	// +optional
	PowerDomains []PublishedPowerDomainStatus `json:"powerDomains,omitempty"`

	// startupWaves is the advisory reverse-order projection for subscriber-owned recovery.
	// +optional
	StartupWaves []CompiledShutdownWave `json:"startupWaves,omitempty"`

	// explanations record planner and edge-level "why" statements.
	// +optional
	Explanations []PlannerExplanationStatus `json:"explanations,omitempty"`

	// diagrams are deterministic renderings generated from graph.
	// +optional
	Diagrams PlannerDiagramExportsStatus `json:"diagrams,omitempty"`
}

// PublishedPowerDomainStatus is one resolver-derived power-domain closure.
type PublishedPowerDomainStatus struct {
	// name is the domain label declared on one or more UPS roots.
	Name string `json:"name"`

	// upsDevices are the UPSDevice roots that feed this domain.
	// +optional
	UPSDevices []string `json:"upsDevices,omitempty"`

	// members are all inventory entities in the derived feeds closure.
	// +optional
	Members []string `json:"members,omitempty"`

	// nodes are Kubernetes nodes in the derived feeds closure.
	// +optional
	Nodes []string `json:"nodes,omitempty"`

	// infrastructure are non-node, non-UPS entities in the derived feeds closure.
	// +optional
	Infrastructure []string `json:"infrastructure,omitempty"`
}

// PlannerGraphStatus is the normalized dependency graph status shape.
type PlannerGraphStatus struct {
	// vertices are graph nodes.
	// +optional
	Vertices []PlannerGraphVertexStatus `json:"vertices,omitempty"`

	// edges are directed dependencies from from to to.
	// +optional
	Edges []PlannerGraphEdgeStatus `json:"edges,omitempty"`
}

// PlannerGraphVertexStatus is one normalized graph vertex.
type PlannerGraphVertexStatus struct {
	// id is the stable graph vertex id.
	ID string `json:"id"`

	// kind identifies the source vertex kind.
	// +optional
	Kind string `json:"kind,omitempty"`

	// label is the human-readable vertex label.
	// +optional
	Label string `json:"label,omitempty"`

	// action is the compiled shutdown action associated with the vertex.
	// +optional
	Action string `json:"action,omitempty"`

	// phase is the shutdown phase hint, when present.
	// +optional
	Phase *int32 `json:"phase,omitempty"`

	// shutdownTier is the effective numbered shutdown tier, when present.
	// +optional
	ShutdownTier *int32 `json:"shutdownTier,omitempty"`

	// targetSummary summarizes selected targets without storing bulky object lists.
	// +optional
	TargetSummary string `json:"targetSummary,omitempty"`
}

// PlannerGraphEdgeStatus is one normalized graph edge.
type PlannerGraphEdgeStatus struct {
	// id is the stable graph edge id.
	ID string `json:"id"`

	// from is the source graph vertex id.
	From string `json:"from"`

	// to is the destination graph vertex id.
	To string `json:"to"`

	// relation is the planner relation that produced this edge.
	// +optional
	Relation string `json:"relation,omitempty"`

	// provenance records whether the edge was declared, derived, or policy-produced.
	// +optional
	Provenance string `json:"provenance,omitempty"`

	// sources identify the declarative fields that produced this edge.
	// +optional
	Sources []PlannerGraphSourceRefStatus `json:"sources,omitempty"`

	// explanation is the stable publishable reason for this edge.
	// +optional
	Explanation string `json:"explanation,omitempty"`
}

// PlannerGraphSourceRefStatus identifies a declarative source for a graph edge.
type PlannerGraphSourceRefStatus struct {
	// kind identifies the source input kind.
	// +optional
	Kind string `json:"kind,omitempty"`

	// name identifies the source input object or item.
	// +optional
	Name string `json:"name,omitempty"`

	// field identifies the source input field.
	// +optional
	Field string `json:"field,omitempty"`
}

// PlannerExplanationStatus is a publishable planner "why" statement.
type PlannerExplanationStatus struct {
	// id is the stable explanation id.
	ID string `json:"id"`

	// subject identifies the graph edge, wave, or planner object explained.
	// +optional
	Subject string `json:"subject,omitempty"`

	// reason is a machine-readable explanation reason.
	// +optional
	Reason string `json:"reason,omitempty"`

	// message is the human-readable explanation.
	Message string `json:"message"`
}

// PlannerDiagramExportsStatus holds deterministic graph renderings.
type PlannerDiagramExportsStatus struct {
	// mermaid is the Mermaid flowchart rendering.
	// +optional
	Mermaid string `json:"mermaid,omitempty"`

	// graphvizDOT is the Graphviz DOT rendering.
	// +optional
	GraphvizDOT string `json:"graphvizDOT,omitempty"`

	// d2 is the D2 rendering.
	// +optional
	D2 string `json:"d2,omitempty"`
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

const (
	// ShutdownFlowRehearsalRequestAnnotation requests one enforce-mode rehearsal execution
	// when set to a non-empty token. Changing the token requests another rehearsal.
	ShutdownFlowRehearsalRequestAnnotation = "power.zalud.io/rehearsal-request"
	// ShutdownFlowRehearsalReasonAnnotation optionally records a human reason for a
	// rehearsal request in the execution audit details.
	ShutdownFlowRehearsalReasonAnnotation = "power.zalud.io/rehearsal-reason"
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
