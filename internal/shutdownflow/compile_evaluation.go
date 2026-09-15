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
	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
)

// CompileWithHistoryLookup compiles with observed durations folded into the
// estimates (EX-32).
//
// Compile identity first: estimates do not contribute to the hash, so history
// can be selected for the new target set even before status has been updated.
func CompileWithHistoryLookup(obj *powerv1alpha1.ShutdownFlow, bundle resolver.StructuralBundle, policy powerv1alpha1.PowerShutdownTierPolicySpec, historyFor func(string) planner.HistoryInputs, hookDigests []planner.HookDigest) CompiledFlow {
	return CompileForEvaluation(obj, bundle, policy, historyFor, hookDigests, nil)
}

// CompileForEvaluation validates the configured plan, then compiles the eligible scope. History
// must be looked up under the hash of the plan that will actually execute.
func CompileForEvaluation(obj *powerv1alpha1.ShutdownFlow, bundle resolver.StructuralBundle, policy powerv1alpha1.PowerShutdownTierPolicySpec, historyFor func(string) planner.HistoryInputs, hookDigests []planner.HookDigest, evaluation *powerv1alpha1.ShutdownTriggerEvaluationStatus) CompiledFlow {
	compiled := CompileFlowWithHistoryAndHooks(obj, bundle, policy, planner.HistoryInputs{}, hookDigests)
	var scope *planner.ExecutionScope
	if compiled.ConfigHash != "" && evaluation != nil && evaluation.Eligible {
		scope = &planner.ExecutionScope{UPSDevices: evaluation.SelectedUPSDevices}
		compiled = CompileFlowWithScope(obj, bundle, policy, planner.HistoryInputs{}, hookDigests, scope)
	}
	if compiled.ConfigHash == "" || historyFor == nil {
		return compiled
	}
	history := historyFor(compiled.ConfigHash)
	if len(history.GroupDurations) == 0 {
		return compiled
	}
	return CompileFlowWithScope(obj, bundle, policy, history, hookDigests, scope)
}
