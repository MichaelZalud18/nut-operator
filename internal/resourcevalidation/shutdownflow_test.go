// Copyright 2026 Michael Zalud.
// SPDX-License-Identifier: Apache-2.0

package resourcevalidation

import (
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/util/validation/field"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

func TestMapValidationErrorOrder(t *testing.T) {
	path := field.NewPath("spec", "groups").Index(0).Child("params")
	for i := 0; i < 100; i++ {
		for _, tc := range []struct {
			errs field.ErrorList
			want []string
		}{
			{ValidateStringMap(path, map[string]string{"z": "\n", "a": "\t"}), []string{"spec.groups[0].params[a]", "spec.groups[0].params[z]"}},
			{ValidateRemovedWorkflowParams(path, powerv1alpha1.ShutdownStepNotify, map[string]string{"workflow.z": "z", "workflow.a": "a"}), []string{"spec.groups[0].params[workflow.a]", "spec.groups[0].params[workflow.z]"}},
		} {
			var got []string
			for _, err := range tc.errs {
				got = append(got, err.Field)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("error fields=%v, want %v", got, tc.want)
			}
		}
	}
}
