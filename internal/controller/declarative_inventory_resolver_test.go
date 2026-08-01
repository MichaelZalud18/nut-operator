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
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/capability"
)

func TestResolveDeclarativeStructuralBundleUsesBundledProfiles(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := powerv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add power API to scheme: %v", err)
	}

	reader := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(
			&powerv1alpha1.UPSDevice{
				ObjectMeta: objectMeta("tower"),
				Spec: powerv1alpha1.UPSDeviceSpec{
					Driver: "dummy-ups",
					Identity: powerv1alpha1.UPSDeviceIdentitySpec{
						Model: "TOWER_1000VA_230V",
					},
					PowerDomains: []string{"rack-a"},
				},
			},
			&powerv1alpha1.PowerInventoryNode{
				ObjectMeta: objectMeta("node-a"),
				Spec: powerv1alpha1.PowerInventoryNodeSpec{
					NodeName:                "node-a",
					CommunicationPathExempt: boolPtr(true),
				},
			},
			&powerv1alpha1.PowerInventoryEdge{
				ObjectMeta: objectMeta("tower-feeds-node-a"),
				Spec: powerv1alpha1.PowerInventoryEdgeSpec{
					From: powerv1alpha1.PowerInventoryEntityReference{
						Kind: powerv1alpha1.PowerInventoryEntityUPSDevice,
						Name: "tower",
					},
					To: powerv1alpha1.PowerInventoryEntityReference{
						Kind: powerv1alpha1.PowerInventoryEntityNode,
						Name: "node-a",
					},
					Relation: powerv1alpha1.PowerInventoryEdgeFeeds,
					Input:    "psu-a",
				},
			},
		).
		Build()

	bundle, diagnostics, err := resolveDeclarativeStructuralBundle(context.Background(), reader)
	if err != nil {
		t.Fatalf("expected bundled profile resolution to succeed, got %v with diagnostics %#v", err, diagnostics)
	}
	if len(bundle.CapabilityMatches) != 1 {
		t.Fatalf("expected one capability match, got %#v", bundle.CapabilityMatches)
	}
	match := bundle.CapabilityMatches[0]
	if match.ProfileID != capability.BundledUbiquitiUPSTowerProfileID {
		t.Fatalf("expected bundled Ubiquiti tower profile, got %#v", match)
	}
	if match.ProfileSource != capability.ProfileSourceBundled {
		t.Fatalf("expected bundled source, got %#v", match)
	}
}

func objectMeta(name string) metav1.ObjectMeta {
	return metav1.ObjectMeta{Name: name}
}
