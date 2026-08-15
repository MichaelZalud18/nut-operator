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
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

var _ = Describe("ShutdownFlow Webhook", func() {
	var (
		obj       *powerv1alpha1.ShutdownFlow
		oldObj    *powerv1alpha1.ShutdownFlow
		validator ShutdownFlowCustomValidator
		defaulter ShutdownFlowCustomDefaulter
	)

	BeforeEach(func() {
		obj = &powerv1alpha1.ShutdownFlow{}
		oldObj = &powerv1alpha1.ShutdownFlow{}
		validator = ShutdownFlowCustomValidator{}
		defaulter = ShutdownFlowCustomDefaulter{}
	})

	Context("When creating ShutdownFlow under Defaulting Webhook", func() {
		It("Should default to dry-run review mode", func() {
			obj.Spec.Steps = []powerv1alpha1.ShutdownStep{{ID: "notify", Type: powerv1alpha1.ShutdownStepNotify}}

			Expect(defaulter.Default(ctx, obj)).To(Succeed())

			Expect(obj.Spec.Mode).To(Equal(powerv1alpha1.ShutdownFlowModeDryRun))
			Expect(obj.Spec.ConcurrencyPolicy).To(Equal("Forbid"))
			Expect(obj.Spec.TierOverrunPolicy).To(Equal(powerv1alpha1.ShutdownTierOverrunWait))
			Expect(obj.Spec.AbortPolicy.Behavior).To(Equal(powerv1alpha1.AbortBehaviorHaltAndSurface))
			Expect(*obj.Spec.AbortPolicy.Notify).To(BeTrue())
			Expect(*obj.Spec.Safety.RequireManualApproval).To(BeTrue())
			Expect(*obj.Spec.Steps[0].ContinueOnError).To(BeFalse())
		})
	})

	Context("When creating or updating ShutdownFlow under Validating Webhook", func() {
		It("Should admit a valid grouped shutdown graph", func() {
			obj.Spec = validShutdownFlowSpec()

			warnings, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
			Expect(warnings).To(BeEmpty())
		})

		It("Should admit a coarser trigger fallback", func() {
			obj.Spec = validShutdownFlowSpec()
			runtimeSeconds := int64(300)
			fallback := powerv1alpha1.ShutdownTriggerLowBattery
			obj.Spec.Triggers[0].Type = powerv1alpha1.ShutdownTriggerRuntimeBelow
			obj.Spec.Triggers[0].RuntimeBelowSeconds = &runtimeSeconds
			obj.Spec.Triggers[0].FallbackType = &fallback

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should admit preempt tier-overrun policy", func() {
			obj.Spec = validShutdownFlowSpec()
			obj.Spec.TierOverrunPolicy = powerv1alpha1.ShutdownTierOverrunPreempt

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should reject an unsupported tier-overrun policy", func() {
			obj.Spec = validShutdownFlowSpec()
			obj.Spec.TierOverrunPolicy = powerv1alpha1.ShutdownTierOverrunPolicy("Overlap")

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("tierOverrunPolicy"))
		})

		It("Should reject a trigger fallback that is not coarser", func() {
			obj.Spec = validShutdownFlowSpec()
			runtimeSeconds := int64(300)
			fallback := powerv1alpha1.ShutdownTriggerChargeBelow
			obj.Spec.Triggers[0].Type = powerv1alpha1.ShutdownTriggerRuntimeBelow
			obj.Spec.Triggers[0].RuntimeBelowSeconds = &runtimeSeconds
			obj.Spec.Triggers[0].FallbackType = &fallback

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("fallbackType"))
		})

		It("Should reject a fallback on a trigger that already needs only ups.status", func() {
			obj.Spec = validShutdownFlowSpec()
			fallback := powerv1alpha1.ShutdownTriggerLowBattery
			obj.Spec.Triggers[0].Type = powerv1alpha1.ShutdownTriggerOnBattery
			obj.Spec.Triggers[0].FallbackType = &fallback

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("no coarser fallback"))
		})

		It("Should reject a fallback equal to the trigger's own type", func() {
			obj.Spec = validShutdownFlowSpec()
			runtimeSeconds := int64(300)
			fallback := powerv1alpha1.ShutdownTriggerRuntimeBelow
			obj.Spec.Triggers[0].Type = powerv1alpha1.ShutdownTriggerRuntimeBelow
			obj.Spec.Triggers[0].RuntimeBelowSeconds = &runtimeSeconds
			obj.Spec.Triggers[0].FallbackType = &fallback

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("covers nothing"))
		})

		It("Should return a planner warning when groups and fallback steps are both present", func() {
			obj.Spec = validShutdownFlowSpec()
			obj.Spec.Steps = []powerv1alpha1.ShutdownStep{{ID: "fallback", Type: powerv1alpha1.ShutdownStepNotify}}

			warnings, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
			Expect(warnings).To(ContainElement(ContainSubstring("linear steps are ignored")))
		})

		It("Should reject unknown graph dependencies through the planner", func() {
			obj.Spec = validShutdownFlowSpec()
			obj.Spec.Groups[0].Before = []string{"missing-group"}

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("unknown dependency"))
		})

		It("Should reject RuntimeBelow triggers without a runtime threshold", func() {
			obj.Spec = validShutdownFlowSpec()
			obj.Spec.Triggers = []powerv1alpha1.ShutdownTrigger{{Type: powerv1alpha1.ShutdownTriggerRuntimeBelow}}

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.triggers[0].runtimeBelowSeconds"))
		})

		It("Should reject enforce mode without manual approval", func() {
			obj.Spec = validShutdownFlowSpec()
			obj.Spec.Mode = powerv1alpha1.ShutdownFlowModeEnforce
			obj.Spec.Safety.ApprovalAnnotation = "power.zalud.io/approved-for-enforce"

			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("metadata.annotations[power.zalud.io/approved-for-enforce]"))
		})

		It("Should admit enforce mode when the explicit approval annotation is true", func() {
			obj.Annotations = map[string]string{"power.zalud.io/approved-for-enforce": "true"}
			obj.Spec = validShutdownFlowSpec()
			obj.Spec.Mode = powerv1alpha1.ShutdownFlowModeEnforce
			obj.Spec.Safety.ApprovalAnnotation = "power.zalud.io/approved-for-enforce"

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should reject compiled plans that exceed the max duration budget", func() {
			obj.Spec = validShutdownFlowSpec()
			obj.Spec.Safety.MaxEstimatedDuration = &metav1.Duration{Duration: time.Minute}

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("maxEstimatedDuration"))
		})
	})
})

func validShutdownFlowSpec() powerv1alpha1.ShutdownFlowSpec {
	return powerv1alpha1.ShutdownFlowSpec{
		Mode: powerv1alpha1.ShutdownFlowModeDryRun,
		Triggers: []powerv1alpha1.ShutdownTrigger{{
			Type:         powerv1alpha1.ShutdownTriggerOnBattery,
			PowerDomains: []string{"rack-a"},
			For:          &metav1.Duration{Duration: 2 * time.Minute},
		}},
		Groups: []powerv1alpha1.ShutdownGroup{
			{
				Name:    "applications",
				Action:  powerv1alpha1.ShutdownStepScaleWorkload,
				Before:  []string{"agent-shutdown"},
				Timeout: &metav1.Duration{Duration: 5 * time.Minute},
			},
			{
				Name:    "agent-shutdown",
				Action:  powerv1alpha1.ShutdownStepAgentShutdown,
				Timeout: &metav1.Duration{Duration: 5 * time.Minute},
			},
		},
		ConcurrencyPolicy: "Forbid",
		TierOverrunPolicy: powerv1alpha1.ShutdownTierOverrunWait,
		AbortPolicy: powerv1alpha1.AbortPolicySpec{
			Behavior: powerv1alpha1.AbortBehaviorHaltAndSurface,
			Notify:   ptrBool(true),
		},
		Safety: powerv1alpha1.FlowSafetySpec{
			RequireManualApproval: ptrBool(true),
			MaxEstimatedDuration:  &metav1.Duration{Duration: 15 * time.Minute},
		},
	}
}

var _ = Describe("RunHook validation", func() {
	It("rejects RunHook without a hookRef", func() {
		errs := validateRunHookReference(
			field.NewPath("spec", "groups").Index(0).Child("hookRef"),
			powerv1alpha1.ShutdownStepRunHook, nil)

		Expect(errs).NotTo(BeEmpty())
		Expect(errs[0].Detail).To(ContainSubstring("RunHook"))
	})

	It("rejects hookRef on non-hook actions", func() {
		errs := validateRunHookReference(
			field.NewPath("spec", "groups").Index(0).Child("hookRef"),
			powerv1alpha1.ShutdownStepNotify,
			&powerv1alpha1.NamespacedNameReference{Namespace: "apps", Name: "flush"})

		Expect(errs).NotTo(BeEmpty())
		Expect(errs[0].Detail).To(ContainSubstring("only valid"))
	})

	It("rejects legacy workflow params", func() {
		errs := validateRemovedWorkflowParams(
			field.NewPath("spec", "groups").Index(0).Child("params"),
			powerv1alpha1.ShutdownStepRunHook,
			map[string]string{"workflow.templateRef": "db-snapshot"})

		Expect(errs).NotTo(BeEmpty())
		Expect(errs[0].Detail).To(ContainSubstring("RunWorkflow was removed"))
	})

	It("requires managementClusterRef when a flow uses RunHook", func() {
		obj := &powerv1alpha1.ShutdownFlow{Spec: validShutdownFlowSpec()}
		obj.Spec.Groups = append(obj.Spec.Groups, powerv1alpha1.ShutdownGroup{
			Name:    "flush-storage",
			Action:  powerv1alpha1.ShutdownStepRunHook,
			HookRef: &powerv1alpha1.NamespacedNameReference{Namespace: "storage", Name: "flush"},
		})

		errs := validateShutdownHookPolicy(field.NewPath("spec"), obj)

		Expect(errs).NotTo(BeEmpty())
		Expect(errs[0].Field).To(ContainSubstring("managementClusterRef"))
	})
})
