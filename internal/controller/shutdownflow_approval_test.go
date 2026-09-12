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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

// F-126: an already-rendered actuator is not current authorization. approvalChecker is the
// executor's live cross-check that a flow is still in Enforce mode, read fresh at each wave
// boundary rather than trusted from the snapshot Execute started with. This proves it against a
// real API server (envtest), not a fake: a checker that only ever reads its own in-memory
// snapshot would pass a unit test against a mock just as easily as a live Get.
var _ = Describe("ShutdownFlow approvalChecker", func() {
	const name = "test-shutdownflow-approval-checker"
	ctx := context.Background()

	minimalEnforceFlow := func() *powerv1alpha1.ShutdownFlow {
		return &powerv1alpha1.ShutdownFlow{
			ObjectMeta: metav1.ObjectMeta{Name: name},
			Spec: powerv1alpha1.ShutdownFlowSpec{
				Mode: powerv1alpha1.ShutdownFlowModeEnforce,
				Triggers: []powerv1alpha1.ShutdownTrigger{{
					Type:         powerv1alpha1.ShutdownTriggerOnBattery,
					PowerDomains: []string{"rack-a"},
				}},
				Groups: []powerv1alpha1.ShutdownGroup{{
					Name:   "applications",
					Action: powerv1alpha1.ShutdownStepScaleWorkload,
				}},
			},
		}
	}

	AfterEach(func() {
		flow := &powerv1alpha1.ShutdownFlow{ObjectMeta: metav1.ObjectMeta{Name: name}}
		_ = k8sClient.Delete(ctx, flow)
	})

	It("re-reads live state rather than trusting the object it was built from", func() {
		By("creating a flow already in Enforce mode")
		flow := minimalEnforceFlow()
		Expect(k8sClient.Create(ctx, flow)).To(Succeed())

		reconciler := &ShutdownFlowReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		checker := reconciler.approvalChecker(flow)

		By("confirming it reads approved while the flow is still Enforce")
		approved, err := checker(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(approved).To(BeTrue())

		By("flipping spec.mode away from Enforce through the API, not through the in-memory flow object the checker closed over")
		var current powerv1alpha1.ShutdownFlow
		Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(flow), &current)).To(Succeed())
		current.Spec.Mode = powerv1alpha1.ShutdownFlowModeDryRun
		Expect(k8sClient.Update(ctx, &current)).To(Succeed())

		By("confirming the SAME checker closure now reports revoked, without being reconstructed")
		approved, err = checker(ctx)
		Expect(err).NotTo(HaveOccurred())
		Expect(approved).To(BeFalse())
	})

	It("treats a missing flow as a read failure, not as approved", func() {
		reconciler := &ShutdownFlowReconciler{Client: k8sClient, Scheme: k8sClient.Scheme()}
		flow := minimalEnforceFlow() // never created
		checker := reconciler.approvalChecker(flow)

		approved, err := checker(ctx)
		Expect(err).To(HaveOccurred())
		Expect(approved).To(BeFalse())
	})
})
