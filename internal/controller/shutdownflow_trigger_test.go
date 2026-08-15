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
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/inventory"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
	triggerpkg "github.com/MichaelZalud18/nut-operator/internal/trigger"
)

func TestTriggerUPSStatesUseDerivedDomainMembership(t *testing.T) {
	states := triggerUPSStatesFromDevices(
		[]powerv1alpha1.UPSDevice{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "ups-a"},
				Spec: powerv1alpha1.UPSDeviceSpec{
					PowerDomains: []string{"authored-domain"},
				},
				Status: powerv1alpha1.UPSDeviceStatus{
					Phase: powerv1alpha1.UPSDevicePhaseOnBattery,
				},
			},
		},
		resolver.StructuralBundle{
			Topology: inventory.Topology{
				Domains: []inventory.PowerDomain{
					{Name: "derived-domain", UPSDevices: []string{"ups-a"}},
					{Name: "other-domain", UPSDevices: []string{"ups-b"}},
				},
			},
		},
	)

	if len(states) != 1 {
		t.Fatalf("expected one UPS state, got %#v", states)
	}
	if !slices.Equal(states[0].PowerDomains, []string{"derived-domain"}) {
		t.Fatalf("expected derived domain membership, got %#v", states[0].PowerDomains)
	}

	wrongDomain := triggerpkg.Evaluate(triggerpkg.Inputs{
		ObservedAt: time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC),
		Triggers: []triggerpkg.Trigger{{
			Type:         triggerpkg.TypeOnBattery,
			PowerDomains: []string{"authored-domain"},
		}},
		UPSStates: states,
	})
	if wrongDomain.Approved {
		t.Fatalf("authored UPSDevice domain labels must not drive trigger eligibility when derived topology exists: %#v", wrongDomain)
	}

	derivedDomain := triggerpkg.Evaluate(triggerpkg.Inputs{
		ObservedAt: time.Date(2026, 8, 15, 12, 0, 0, 0, time.UTC),
		Triggers: []triggerpkg.Trigger{{
			Type:         triggerpkg.TypeOnBattery,
			PowerDomains: []string{"derived-domain"},
		}},
		UPSStates: states,
	})
	if !derivedDomain.Approved {
		t.Fatalf("derived domain membership should drive trigger eligibility, got %#v", derivedDomain)
	}
}

func TestTriggerUPSStatesFallBackToSpecDomainsWithoutDerivedTopology(t *testing.T) {
	states := triggerUPSStatesFromDevices(
		[]powerv1alpha1.UPSDevice{
			{
				ObjectMeta: metav1.ObjectMeta{Name: "ups-a"},
				Spec: powerv1alpha1.UPSDeviceSpec{
					PowerDomains: []string{"authored-domain"},
				},
				Status: powerv1alpha1.UPSDeviceStatus{
					Phase: powerv1alpha1.UPSDevicePhaseOnBattery,
				},
			},
		},
		resolver.StructuralBundle{},
	)

	if len(states) != 1 {
		t.Fatalf("expected one UPS state, got %#v", states)
	}
	if !slices.Equal(states[0].PowerDomains, []string{"authored-domain"}) {
		t.Fatalf("expected authored domain fallback, got %#v", states[0].PowerDomains)
	}
}
