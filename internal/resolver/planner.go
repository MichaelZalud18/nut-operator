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

package resolver

import "github.com/MichaelZalud18/nut-operator/internal/planner"

// AttachResolvedInputHash marks planner structural input with the resolver
// bundle hash so inventory and capability changes participate in plan identity.
func AttachResolvedInputHash(flow planner.StructuralInputs, bundle StructuralBundle) planner.StructuralInputs {
	flow.ResolvedInputHash = bundle.Hash
	if flow.ObservedAt == "" {
		flow.ObservedAt = bundle.ObservedAt
	}
	return flow
}
