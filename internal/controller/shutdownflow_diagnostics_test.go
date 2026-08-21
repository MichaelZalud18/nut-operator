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
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
)

// F-99: a rejected compile reported reason PlannerFailed and message "shutdown flow planner failed
// after resolver inputs were attached", which names the component and not the cause. The cause was
// in a diagnostic that only ever reached the audit writer.
func TestPlannerRejectionNamesThePlannersOwnReason(t *testing.T) {
	result := plannerRejection([]planner.Diagnostic{
		{Severity: planner.DiagnosticWarning, Reason: "EstimateUnverified", Message: "no execution history"},
		{Severity: planner.DiagnosticError, Reason: "TriggerUnsupportedByProfile", Subject: "sim-homelab-ups", Message: "RuntimeBelow requires battery.runtime, which the matched profile does not declare"},
	})

	if result.accepted {
		t.Fatal("a planner rejection was reported as accepted")
	}
	if result.reason != "TriggerUnsupportedByProfile" {
		t.Errorf("reason = %q, want the planner's own reason", result.reason)
	}
	if !strings.Contains(result.message, "battery.runtime") {
		t.Errorf("message does not carry the diagnostic: %q", result.message)
	}
	// The subject is the difference between "a trigger is unsupported" and "this device's trigger
	// is unsupported", which is what tells the reader where to go.
	if !strings.Contains(result.message, "sim-homelab-ups") {
		t.Errorf("message does not name the subject: %q", result.message)
	}
}

// A planner that produces neither a plan nor a diagnostic is its own bug. Flattening it into a
// specific-sounding reason would hide it, so the generic reason survives as the fallback and says
// plainly that nothing explained the rejection.
func TestPlannerRejectionFallsBackWhenNothingExplainedIt(t *testing.T) {
	result := plannerRejection(nil)

	if result.reason != "PlannerFailed" {
		t.Errorf("reason = %q, want PlannerFailed", result.reason)
	}
	if !strings.Contains(result.message, "no diagnostic") {
		t.Errorf("the fallback must say the planner explained nothing: %q", result.message)
	}
}

// Warnings alone are not a rejection. plannerRejection is only reached when no plan was produced,
// and picking a warning as the reason would report an advisory finding as the cause.
func TestPlannerRejectionIgnoresWarnings(t *testing.T) {
	result := plannerRejection([]planner.Diagnostic{
		{Severity: planner.DiagnosticWarning, Reason: "EstimateUnverified", Message: "no execution history"},
	})

	if result.reason != "PlannerFailed" {
		t.Errorf("reason = %q, want the fallback -- a warning is not why the compile was rejected", result.reason)
	}
}

// Published on the resource because the audit writer cannot be relied on to carry them: it returns
// early when spec.managementClusterRef is unset and writes nowhere at all under storage: Disabled,
// which is the mode the install guide recommends for evaluation.
func TestCompileDiagnosticStatusesCarryBothStages(t *testing.T) {
	statuses := compileDiagnosticStatuses(
		[]resolver.Diagnostic{{
			Severity: resolver.DiagnosticError,
			Source:   resolver.DiagnosticSourceInventory,
			Reason:   "FeedInputRequired",
			Subject:  "PowerInventoryEdge/rack-a",
			Message:  "feeds edge requires an input qualifier",
		}},
		[]planner.Diagnostic{{
			Severity: planner.DiagnosticError,
			Reason:   "TriggerUnsupportedByProfile",
			Subject:  "sim-homelab-ups",
			Message:  "RuntimeBelow requires battery.runtime",
		}},
		nil,
	)

	if len(statuses) != 2 {
		t.Fatalf("got %d diagnostics, want both stages: %#v", len(statuses), statuses)
	}
	if statuses[0].Source != resolver.DiagnosticSourceInventory {
		t.Errorf("resolver diagnostic source = %q, want %q", statuses[0].Source, resolver.DiagnosticSourceInventory)
	}
	// Tagged, because an inventory problem and a planning problem need different fixes and the
	// reason strings alone do not say which stage produced them.
	if statuses[1].Source != "Planner" {
		t.Errorf("planner diagnostic source = %q, want Planner", statuses[1].Source)
	}
	if statuses[1].Reason != "TriggerUnsupportedByProfile" || statuses[1].Subject != "sim-homelab-ups" {
		t.Errorf("planner diagnostic lost its detail: %#v", statuses[1])
	}
}

func TestCompileDiagnosticStatusesAreNilWhenThereAreNone(t *testing.T) {
	if statuses := compileDiagnosticStatuses(nil, nil, nil); statuses != nil {
		t.Errorf("a clean compile published %#v, want nothing", statuses)
	}
}

// F-113: the allowlist was enforced only at delivery time, so a hook pointing somewhere
// `spec.hooks.allowedEndpoints` does not cover compiled clean and failed mid-outage — the one
// moment `HK-9` exists to have settled in Git beforehand.
func TestDisallowedHookEndpointIsReportedAtCompileTime(t *testing.T) {
	cluster := &powerv1alpha1.PowerManagementCluster{
		ObjectMeta: metav1.ObjectMeta{Name: "power"},
		Spec: powerv1alpha1.PowerManagementClusterSpec{
			Hooks: powerv1alpha1.PowerHookPolicySpec{
				AllowedEndpoints: []powerv1alpha1.PowerHookEndpointAllowlistEntry{
					{Scheme: "https", Host: "hooks.example.test", PathPrefix: "/power"},
				},
			},
		},
	}
	ref := powerv1alpha1.NamespacedNameReference{Namespace: "apps", Name: "snapshot"}
	hook := &powerv1alpha1.ShutdownHook{
		Spec: powerv1alpha1.ShutdownHookSpec{
			Invocation: powerv1alpha1.ShutdownHookInvocationSpec{
				Transport: powerv1alpha1.ShutdownHookTransportHTTP,
				HTTP:      &powerv1alpha1.ShutdownHookHTTPSpec{URL: "https://elsewhere.example.test/power/snapshot"},
			},
		},
	}

	diagnostics := disallowedHookEndpointDiagnostics(hook, ref, cluster)

	if len(diagnostics) != 1 {
		t.Fatalf("got %d diagnostics, want one: %#v", len(diagnostics), diagnostics)
	}
	if diagnostics[0].Reason != "HookEndpointNotAllowed" {
		t.Errorf("reason = %q, want HookEndpointNotAllowed", diagnostics[0].Reason)
	}
	// Warning, not Error: OD-34 makes hooks advisory and HK-7 says a hook never holds a wave, so an
	// undeliverable hook must not stop a shutdown plan from existing.
	if diagnostics[0].Severity != resolver.DiagnosticWarning {
		t.Errorf("severity = %q, want Warning -- rejecting the flow would let a hook hold a shutdown", diagnostics[0].Severity)
	}
	if !strings.Contains(diagnostics[0].Subject, "apps/snapshot") {
		t.Errorf("subject does not name the hook: %q", diagnostics[0].Subject)
	}
}

// The dry-run invocation is checked too. A rehearsal that cannot reach its endpoint proves nothing,
// and finding that out during the rehearsal defeats the point of having one.
func TestDisallowedHookEndpointChecksTheDryRunInvocation(t *testing.T) {
	cluster := &powerv1alpha1.PowerManagementCluster{
		Spec: powerv1alpha1.PowerManagementClusterSpec{
			Hooks: powerv1alpha1.PowerHookPolicySpec{
				AllowedEndpoints: []powerv1alpha1.PowerHookEndpointAllowlistEntry{
					{Scheme: "https", Host: "hooks.example.test"},
				},
			},
		},
	}
	hook := &powerv1alpha1.ShutdownHook{
		Spec: powerv1alpha1.ShutdownHookSpec{
			Invocation: powerv1alpha1.ShutdownHookInvocationSpec{
				HTTP: &powerv1alpha1.ShutdownHookHTTPSpec{URL: "https://hooks.example.test/go"},
			},
			DryRun: &powerv1alpha1.ShutdownHookInvocationSpec{
				HTTP: &powerv1alpha1.ShutdownHookHTTPSpec{URL: "https://staging.example.test/go"},
			},
		},
	}

	diagnostics := disallowedHookEndpointDiagnostics(hook, powerv1alpha1.NamespacedNameReference{Name: "h"}, cluster)

	if len(diagnostics) != 1 {
		t.Fatalf("got %d diagnostics, want one for the dryRun URL: %#v", len(diagnostics), diagnostics)
	}
	if !strings.Contains(diagnostics[0].Message, "dryRun") {
		t.Errorf("message does not identify which invocation is at fault: %q", diagnostics[0].Message)
	}
}

func TestAllowedHookEndpointProducesNoDiagnostic(t *testing.T) {
	cluster := &powerv1alpha1.PowerManagementCluster{
		Spec: powerv1alpha1.PowerManagementClusterSpec{
			Hooks: powerv1alpha1.PowerHookPolicySpec{
				AllowedEndpoints: []powerv1alpha1.PowerHookEndpointAllowlistEntry{
					{Scheme: "https", Host: "hooks.example.test", PathPrefix: "/power"},
				},
			},
		},
	}
	hook := &powerv1alpha1.ShutdownHook{
		Spec: powerv1alpha1.ShutdownHookSpec{
			Invocation: powerv1alpha1.ShutdownHookInvocationSpec{
				HTTP: &powerv1alpha1.ShutdownHookHTTPSpec{URL: "https://hooks.example.test/power/snapshot"},
			},
		},
	}

	if diagnostics := disallowedHookEndpointDiagnostics(hook, powerv1alpha1.NamespacedNameReference{Name: "h"}, cluster); len(diagnostics) != 0 {
		t.Errorf("a permitted hook produced %#v", diagnostics)
	}
}

// A KubernetesObject hook has no URL and must not be reported as an unparseable one.
func TestKubernetesObjectHookIsNotURLChecked(t *testing.T) {
	cluster := &powerv1alpha1.PowerManagementCluster{}
	hook := &powerv1alpha1.ShutdownHook{
		Spec: powerv1alpha1.ShutdownHookSpec{
			Invocation: powerv1alpha1.ShutdownHookInvocationSpec{
				Transport: powerv1alpha1.ShutdownHookTransportKubernetesObject,
			},
		},
	}

	if diagnostics := disallowedHookEndpointDiagnostics(hook, powerv1alpha1.NamespacedNameReference{Name: "h"}, cluster); len(diagnostics) != 0 {
		t.Errorf("a non-HTTP hook produced %#v", diagnostics)
	}
}
