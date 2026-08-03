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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

var _ = Describe("PowerManagementCluster Webhook", func() {
	var (
		obj       *powerv1alpha1.PowerManagementCluster
		oldObj    *powerv1alpha1.PowerManagementCluster
		validator PowerManagementClusterCustomValidator
		defaulter PowerManagementClusterCustomDefaulter
	)

	BeforeEach(func() {
		obj = &powerv1alpha1.PowerManagementCluster{}
		oldObj = &powerv1alpha1.PowerManagementCluster{}
		validator = PowerManagementClusterCustomValidator{}
		defaulter = PowerManagementClusterCustomDefaulter{}
	})

	Context("When creating PowerManagementCluster under Defaulting Webhook", func() {
		It("Should default strict control-plane settings", func() {
			Expect(defaulter.Default(ctx, obj)).To(Succeed())

			Expect(obj.Spec.OperandNamespace).NotTo(BeNil())
			Expect(obj.Spec.OperandNamespace.Name).To(Equal("power-system"))
			Expect(obj.Spec.OperandNamespace.Create).NotTo(BeNil())
			Expect(*obj.Spec.OperandNamespace.Create).To(BeTrue())
			Expect(obj.Spec.Storage.Mode).To(Equal(powerv1alpha1.PowerStorageCNPG))
			Expect(obj.Spec.ShutdownTiers.LabelKey).To(Equal(powerv1alpha1.DefaultShutdownTierLabelKey))
			Expect(obj.Spec.Security.Profile).To(Equal(powerv1alpha1.PowerSecurityRestricted))
			Expect(*obj.Spec.Security.RequireExplicitActuation).To(BeTrue())
			Expect(*obj.Spec.Security.DefaultPodHardening.ReadOnlyRootFilesystem).To(BeTrue())
			Expect(obj.Spec.Security.DefaultPodHardening.SeccompProfileType).To(Equal("RuntimeDefault"))
			Expect(*obj.Spec.Security.DefaultPodHardening.RunAsNonRoot).To(BeTrue())
			Expect(*obj.Spec.Observability.ServiceMonitor).To(BeTrue())
			Expect(*obj.Spec.Observability.KubernetesEvents).To(BeTrue())
		})

		It("Should default PostgreSQL identifiers for configured backends", func() {
			obj.Spec.Storage.Mode = powerv1alpha1.PowerStorageCNPG
			obj.Spec.Storage.CNPG = &powerv1alpha1.CNPGStorageSpec{
				ClusterRef: powerv1alpha1.NamespacedNameReference{
					Namespace: "power-system",
					Name:      "power-audit",
				},
			}

			Expect(defaulter.Default(ctx, obj)).To(Succeed())
			Expect(obj.Spec.Storage.CNPG.Database).To(Equal("power"))
			Expect(obj.Spec.Storage.CNPG.Schema).To(Equal("power"))
		})
	})

	Context("When creating or updating PowerManagementCluster under Validating Webhook", func() {
		It("Should admit configured CNPG storage", func() {
			obj.Spec.Storage.Mode = powerv1alpha1.PowerStorageCNPG
			obj.Spec.Storage.CNPG = &powerv1alpha1.CNPGStorageSpec{
				ClusterRef: powerv1alpha1.NamespacedNameReference{
					Namespace: "power-system",
					Name:      "power-audit",
				},
				Database: "power",
				Schema:   "power",
			}

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should reject CNPG mode without a CNPG cluster reference", func() {
			obj.Spec.Storage.Mode = powerv1alpha1.PowerStorageCNPG

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.storage.cnpg"))
		})

		It("Should reject conflicting storage backend configuration", func() {
			obj.Spec.Storage.Mode = powerv1alpha1.PowerStorageExternalPostgres
			obj.Spec.Storage.CNPG = &powerv1alpha1.CNPGStorageSpec{}
			obj.Spec.Storage.ExternalPostgres = &powerv1alpha1.ExternalPostgresStorageSpec{
				DSNSecretKeyRef: powerv1alpha1.SecretKeyReference{
					Namespace: "power-system",
					Name:      "power-postgres",
					Key:       "dsn",
				},
				Schema: "power",
			}

			_, err := validator.ValidateUpdate(ctx, oldObj, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.storage.cnpg"))
		})

		It("Should warn when durable storage is disabled", func() {
			obj.Spec.Storage.Mode = powerv1alpha1.PowerStorageDisabled

			warnings, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
			Expect(warnings).To(ContainElement(ContainSubstring("Disabled")))
		})

		It("Should admit numbered shutdown tier policy", func() {
			defaultTier := int32(4)
			obj.Spec.Storage.Mode = powerv1alpha1.PowerStorageDisabled
			obj.Spec.ShutdownTiers = powerv1alpha1.PowerShutdownTierPolicySpec{
				LabelKey:    powerv1alpha1.DefaultShutdownTierLabelKey,
				DefaultTier: &defaultTier,
				Tiers: []powerv1alpha1.PowerShutdownTierDefinition{
					{Tier: 0, Name: "last-ditch"},
					{Tier: 1, Name: "nodes"},
					{Tier: 4, Name: "default"},
				},
				SelectorRules: []powerv1alpha1.PowerShutdownTierSelectorRule{{
					Name:    "workloads",
					Subject: powerv1alpha1.PowerShutdownTierSubjectWorkload,
					Tier:    4,
				}},
			}

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).NotTo(HaveOccurred())
		})

		It("Should reject reserved default shutdown tiers", func() {
			defaultTier := int32(1)
			obj.Spec.Storage.Mode = powerv1alpha1.PowerStorageDisabled
			obj.Spec.ShutdownTiers.DefaultTier = &defaultTier

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.shutdownTiers.defaultTier"))
		})

		It("Should reject node selector rules that assign tier zero", func() {
			obj.Spec.Storage.Mode = powerv1alpha1.PowerStorageDisabled
			obj.Spec.ShutdownTiers.SelectorRules = []powerv1alpha1.PowerShutdownTierSelectorRule{{
				Name:    "nodes",
				Subject: powerv1alpha1.PowerShutdownTierSubjectNode,
				Tier:    0,
			}}

			_, err := validator.ValidateCreate(ctx, obj)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("spec.shutdownTiers.selectorRules[0].tier"))
		})
	})
})
