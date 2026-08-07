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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
	shutdownflowadapter "github.com/MichaelZalud18/nut-operator/internal/shutdownflow"
)

func compileShutdownFlow(obj *powerv1alpha1.ShutdownFlow) ([]powerv1alpha1.CompiledShutdownStep, []powerv1alpha1.CompiledShutdownWave, *metav1.Duration, string) {
	return shutdownflowadapter.Compile(obj)
}

func compileShutdownFlowWithResolvedInputsAndTierPolicy(obj *powerv1alpha1.ShutdownFlow, bundle resolver.StructuralBundle, policy powerv1alpha1.PowerShutdownTierPolicySpec) shutdownflowadapter.CompiledFlow {
	return shutdownflowadapter.CompileFlow(obj, bundle, policy)
}
