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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

func namespaceWithLabels(name string, labels map[string]string) *corev1.Namespace {
	return &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels}}
}

// F-62: the levels that reject the actuating pod are the ones worth reporting, and no others.
//
// The measurements behind the table were taken on kind against namespaces carrying each label,
// applying the pod exactly as nodepoweragent_render.go renders it: baseline refused it on
// `hostPID=true` and on `SYS_BOOT`, and restricted refused it for the same reasons plus its own.
func TestPodSecurityConflictReportsOnlyEnforcedRejectingLevels(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		labels   map[string]string
		conflict bool
	}{
		{name: "unlabelled namespace admits everything", labels: nil, conflict: false},
		{name: "privileged admits the actuating pod", labels: map[string]string{"pod-security.kubernetes.io/enforce": "privileged"}, conflict: false},
		{name: "baseline rejects it", labels: map[string]string{"pod-security.kubernetes.io/enforce": "baseline"}, conflict: true},
		{name: "restricted rejects it", labels: map[string]string{"pod-security.kubernetes.io/enforce": "restricted"}, conflict: true},
		// warn and audit produce messages and admit the pod. Reporting Degraded for a namespace that
		// still runs the workload would be a false alarm on the one condition an operator is meant
		// to trust.
		{name: "warn alone does not reject", labels: map[string]string{"pod-security.kubernetes.io/warn": "restricted"}, conflict: false},
		{name: "audit alone does not reject", labels: map[string]string{"pod-security.kubernetes.io/audit": "restricted"}, conflict: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			conflict := nodePowerAgentPodSecurityConflict(namespaceWithLabels("power-system", testCase.labels))
			if testCase.conflict && conflict == "" {
				t.Fatal("expected a conflict to be reported")
			}
			if !testCase.conflict && conflict != "" {
				t.Fatalf("expected no conflict, got: %s", conflict)
			}
		})
	}
}

// The message exists to be read by someone whose DaemonSet has no pods and no obvious reason why.
// If it does not name both violations and the way out, it has not done its job.
func TestPodSecurityConflictNamesBothViolationsAndTheExceptionNeeded(t *testing.T) {
	conflict := nodePowerAgentPodSecurityConflict(namespaceWithLabels("power-system", map[string]string{
		"pod-security.kubernetes.io/enforce": "baseline",
	}))

	for _, want := range []string{"power-system", "baseline", "hostPID", "SYS_BOOT", "privileged"} {
		if !strings.Contains(conflict, want) {
			t.Errorf("the message must mention %q, got: %s", want, conflict)
		}
	}
}

// The stub default is admitted by `restricted`, so a cluster can enforce the strictest level there
// is and still run the agent. Reporting a conflict for it would push users toward relaxing a
// namespace that never needed relaxing.
func TestPodSecurityConflictIsNotReportedForTheNonActuatingDefault(t *testing.T) {
	agent := &powerv1alpha1.NodePowerAgent{ObjectMeta: metav1.ObjectMeta{Name: "default-agent"}}
	if nodePowerAgentRequiresHostPoweroff(agent) {
		t.Fatal("the default agent must not require host poweroff")
	}

	context := actuatorContainerSecurityContext(false)
	if context.Capabilities == nil || len(context.Capabilities.Add) != 0 {
		t.Errorf("the non-actuating actuator must add no capabilities, got: %+v", context.Capabilities)
	}
}
