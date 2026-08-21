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
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

func setAcceptedCondition(conditions *[]metav1.Condition, generation int64, result validationResult) {
	status := metav1.ConditionTrue
	if !result.accepted {
		status = metav1.ConditionFalse
	}

	meta.SetStatusCondition(conditions, metav1.Condition{
		Type:               powerv1alpha1.ConditionAccepted,
		Status:             status,
		ObservedGeneration: generation,
		Reason:             result.reason,
		Message:            result.message,
	})
}

func setReadyCondition(conditions *[]metav1.Condition, generation int64, ready bool, reason, message string) {
	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}

	meta.SetStatusCondition(conditions, metav1.Condition{
		Type:               powerv1alpha1.ConditionReady,
		Status:             status,
		ObservedGeneration: generation,
		Reason:             reason,
		Message:            message,
	})
}

func setDegradedCondition(conditions *[]metav1.Condition, generation int64, degraded bool, reason, message string) {
	status := metav1.ConditionFalse
	if degraded {
		status = metav1.ConditionTrue
	}

	meta.SetStatusCondition(conditions, metav1.Condition{
		Type:               powerv1alpha1.ConditionDegraded,
		Status:             status,
		ObservedGeneration: generation,
		Reason:             reason,
		Message:            message,
	})
}

func setTriggerEligibleCondition(conditions *[]metav1.Condition, generation int64, eligible bool, reason, message string) {
	status := metav1.ConditionFalse
	if eligible {
		status = metav1.ConditionTrue
	}

	meta.SetStatusCondition(conditions, metav1.Condition{
		Type:               powerv1alpha1.ConditionTriggerEligible,
		Status:             status,
		ObservedGeneration: generation,
		Reason:             reason,
		Message:            message,
	})
}

func setExecutionReadyCondition(conditions *[]metav1.Condition, generation int64, ready bool, reason, message string) {
	status := metav1.ConditionFalse
	if ready {
		status = metav1.ConditionTrue
	}

	meta.SetStatusCondition(conditions, metav1.Condition{
		Type:               powerv1alpha1.ConditionExecutionReady,
		Status:             status,
		ObservedGeneration: generation,
		Reason:             reason,
		Message:            message,
	})
}

// setIdentityVerifiedCondition takes the status directly rather than a bool, because Unknown is a
// real answer here: a device with no declared model, or one that has not reported yet, has nothing
// to verify and must not present as either verified or mismatched.
func setIdentityVerifiedCondition(conditions *[]metav1.Condition, generation int64, status metav1.ConditionStatus, reason, message string) {
	meta.SetStatusCondition(conditions, metav1.Condition{
		Type:               powerv1alpha1.ConditionIdentityVerified,
		Status:             status,
		ObservedGeneration: generation,
		Reason:             reason,
		Message:            message,
	})
}
