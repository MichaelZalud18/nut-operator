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
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	webhooks "github.com/MichaelZalud18/nut-operator/internal/webhook/v1alpha1"
)

func TestNodePowerAgentValidationParity(t *testing.T) {
	cases := []struct {
		name, field string
		mutate      func(*powerv1alpha1.NodePowerAgent)
	}{
		{"valid Talos", "", func(*powerv1alpha1.NodePowerAgent) {}},
		{"defaults", "", func(a *powerv1alpha1.NodePowerAgent) {
			a.Spec.Mode = ""
			a.Spec.Shutdown = powerv1alpha1.AgentShutdownSpec{}
		}},
		{"missing server", "spec.nutServerRefs", func(a *powerv1alpha1.NodePowerAgent) { a.Spec.NUTServerRefs = nil }},
		{"bad server name", "spec.nutServerRefs[0].name", func(a *powerv1alpha1.NodePowerAgent) { a.Spec.NUTServerRefs[0].Name = "BAD NAME" }},
		{"reserved namespace", "spec.namespace", func(a *powerv1alpha1.NodePowerAgent) { a.Spec.Namespace = "kube-system" }},
		{"bad mode", "spec.mode", func(a *powerv1alpha1.NodePowerAgent) { a.Spec.Mode = "unknown" }},
		{"approval revoked", "metadata.annotations", func(a *powerv1alpha1.NodePowerAgent) { a.Annotations = nil }},
		{"wrong mode", "spec.shutdown.actuatorPolicy", func(a *powerv1alpha1.NodePowerAgent) { a.Spec.Mode = powerv1alpha1.NodePowerAgentModeDryRun }},
		{"bad policy", "spec.shutdown.actuatorPolicy", func(a *powerv1alpha1.NodePowerAgent) { a.Spec.Shutdown.ActuatorPolicy = "unknown" }},
		{"missing Talos config", "spec.shutdown.talos", func(a *powerv1alpha1.NodePowerAgent) { a.Spec.Shutdown.Talos = nil }},
		{"hostname endpoint", "spec.shutdown.talos.endpoints[0]", func(a *powerv1alpha1.NodePowerAgent) { a.Spec.Shutdown.Talos.Endpoints = []string{"talos.example.net"} }},
		{"short TTL", "spec.shutdown.signalTTL", func(a *powerv1alpha1.NodePowerAgent) {
			a.Spec.Shutdown.SignalTTL = &metav1.Duration{Duration: time.Second}
		}},
		{"relative signal path", "spec.shutdown.signalPath", func(a *powerv1alpha1.NodePowerAgent) { a.Spec.Shutdown.SignalPath = "relative" }},
		{"negative poll", "spec.upsmon.pollFrequency", func(a *powerv1alpha1.NodePowerAgent) {
			a.Spec.Upsmon.PollFrequency = &metav1.Duration{Duration: -time.Second}
		}},
		{"upsmon registry dependency", "spec.images.upsmon.pullPolicy", func(a *powerv1alpha1.NodePowerAgent) { a.Spec.Images.Upsmon.PullPolicy = corev1.PullAlways }},
		{"actuator registry dependency", "spec.images.actuator.pullPolicy", func(a *powerv1alpha1.NodePowerAgent) { a.Spec.Images.Actuator.PullPolicy = corev1.PullAlways }},
	}
	for _, tc := range cases {
		for _, defaulted := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/raw", true: "/defaulted"}[defaulted], func(t *testing.T) {
				a := validTalosNodePowerAgent()
				tc.mutate(a)
				if defaulted {
					if err := (&webhooks.NodePowerAgentCustomDefaulter{}).Default(context.Background(), a); err != nil {
						t.Fatal(err)
					}
				}
				before := a.DeepCopy()
				v := &webhooks.NodePowerAgentCustomValidator{}
				_, createErr := v.ValidateCreate(context.Background(), a)
				_, updateErr := v.ValidateUpdate(context.Background(), validTalosNodePowerAgent(), a)
				assertValidationParity(t, tc.field, validateNodePowerAgent(a), createErr, updateErr)
				if !reflect.DeepEqual(before, a) {
					t.Fatal("validation mutated the resource")
				}
			})
		}
	}
}

func TestShutdownFlowValidationParity(t *testing.T) {
	falseValue := false
	cases := []struct {
		name, field string
		mutate      func(*powerv1alpha1.ShutdownFlow)
	}{
		{"valid", "", func(*powerv1alpha1.ShutdownFlow) {}},
		{"defaults", "", func(f *powerv1alpha1.ShutdownFlow) { f.Spec.Mode = "" }},
		{"missing triggers", "spec.triggers", func(f *powerv1alpha1.ShutdownFlow) { f.Spec.Triggers = nil }},
		{"bad mode", "spec.mode", func(f *powerv1alpha1.ShutdownFlow) { f.Spec.Mode = "unknown" }},
		{"bad trigger", "spec.triggers[0].type", func(f *powerv1alpha1.ShutdownFlow) { f.Spec.Triggers[0].Type = "unknown" }},
		{"missing threshold", "spec.triggers[0].runtimeBelowSeconds", func(f *powerv1alpha1.ShutdownFlow) {
			f.Spec.Triggers[0].Type = powerv1alpha1.ShutdownTriggerRuntimeBelow
		}},
		{"bad concurrency", "spec.concurrencyPolicy", func(f *powerv1alpha1.ShutdownFlow) { f.Spec.ConcurrencyPolicy = "Allow" }},
		{"bad abort", "spec.abortPolicy.behavior", func(f *powerv1alpha1.ShutdownFlow) { f.Spec.AbortPolicy.Behavior = "unknown" }},
		{"bad overrun", "spec.tierOverrunPolicy", func(f *powerv1alpha1.ShutdownFlow) { f.Spec.TierOverrunPolicy = "unknown" }},
		{"missing hook", "spec.groups[0].hookRef", func(f *powerv1alpha1.ShutdownFlow) { f.Spec.Groups[0].Action = powerv1alpha1.ShutdownStepRunHook }},
		{"misplaced hook", "spec.groups[0].hookRef", func(f *powerv1alpha1.ShutdownFlow) {
			f.Spec.Groups[0].HookRef = &powerv1alpha1.NamespacedNameReference{Namespace: "power", Name: "hook"}
		}},
		{"removed params", "spec.groups[0].params", func(f *powerv1alpha1.ShutdownFlow) {
			f.Spec.Groups[0].Params = map[string]string{"workflow.template": "old"}
		}},
		{"unknown dependency", "spec", func(f *powerv1alpha1.ShutdownFlow) { f.Spec.Groups[0].After = []string{"missing"} }},
		{"no approval", "spec.safety.approvalAnnotation", func(f *powerv1alpha1.ShutdownFlow) { f.Spec.Mode = powerv1alpha1.ShutdownFlowModeEnforce }},
		{"approval bypass", "spec.safety.requireManualApproval", func(f *powerv1alpha1.ShutdownFlow) {
			f.Spec.Mode = powerv1alpha1.ShutdownFlowModeEnforce
			f.Spec.Safety.RequireManualApproval = &falseValue
		}},
		{"duration budget", "spec.safety.maxEstimatedDuration", func(f *powerv1alpha1.ShutdownFlow) {
			f.Spec.Groups[0].Timeout = &metav1.Duration{Duration: time.Minute}
			f.Spec.Safety.MaxEstimatedDuration = &metav1.Duration{Duration: time.Second}
		}},
	}
	for _, tc := range cases {
		for _, defaulted := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/raw", true: "/defaulted"}[defaulted], func(t *testing.T) {
				f := shutdownFlowWithGroups([]powerv1alpha1.ShutdownGroup{{Name: "notify", Action: powerv1alpha1.ShutdownStepNotify}})
				tc.mutate(f)
				if defaulted {
					if err := (&webhooks.ShutdownFlowCustomDefaulter{}).Default(context.Background(), f); err != nil {
						t.Fatal(err)
					}
				}
				before := f.DeepCopy()
				v := &webhooks.ShutdownFlowCustomValidator{}
				_, createErr := v.ValidateCreate(context.Background(), f)
				_, updateErr := v.ValidateUpdate(context.Background(), &powerv1alpha1.ShutdownFlow{}, f)
				assertValidationParity(t, tc.field, validateShutdownFlow(f), createErr, updateErr)
				if !reflect.DeepEqual(before, f) {
					t.Fatal("validation mutated the resource")
				}
			})
		}
	}
}

func assertValidationParity(t *testing.T, field string, result validationResult, errs ...error) {
	t.Helper()
	if result.accepted != (field == "") {
		t.Fatalf("accepted=%v reason=%s message=%s", result.accepted, result.reason, result.message)
	}
	for _, err := range errs {
		if (err == nil) != result.accepted {
			t.Fatalf("admission=%v controller=%+v", err, result)
		}
		if field != "" && (!apierrors.IsInvalid(err) || !strings.Contains(err.Error(), field) || !strings.Contains(result.message, field)) {
			t.Fatalf("missing field %s: admission=%v controller=%+v", field, err, result)
		}
	}
}

func TestBypassedAdmissionPublishesStaticRejection(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := powerv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	a := validTalosNodePowerAgent()
	a.Spec.Shutdown.SignalTTL = &metav1.Duration{Duration: time.Second}
	f := shutdownFlowWithGroups([]powerv1alpha1.ShutdownGroup{{Name: "notify", Action: powerv1alpha1.ShutdownStepNotify}})
	f.Name = "bad-flow"
	f.Spec.ConcurrencyPolicy = "Allow"
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(a, f).WithObjects(a, f).Build()
	if _, err := (&NodePowerAgentReconciler{Client: c, Scheme: scheme}).Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: a.Name}}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&ShutdownFlowReconciler{Client: c, Scheme: scheme, runs: newFlowRuns(maxConcurrentFlowRuns)}).Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: f.Name}}); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, types.NamespacedName{Name: a.Name}, a); err != nil {
		t.Fatal(err)
	}
	if err := c.Get(ctx, types.NamespacedName{Name: f.Name}, f); err != nil {
		t.Fatal(err)
	}
	for name, conditions := range map[string][]metav1.Condition{a.Name: a.Status.Conditions, f.Name: f.Status.Conditions} {
		condition := meta.FindStatusCondition(conditions, powerv1alpha1.ConditionAccepted)
		if condition == nil || condition.Status != metav1.ConditionFalse || condition.Reason != "InvalidSpec" {
			t.Fatalf("%s conditions=%+v", name, conditions)
		}
	}
}
