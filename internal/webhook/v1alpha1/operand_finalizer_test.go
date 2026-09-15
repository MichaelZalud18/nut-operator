package v1alpha1

import (
	"context"
	"encoding/json"
	"testing"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	admissionv1 "k8s.io/api/admission/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

func TestLegacyOperandFinalizerRemovalAdmission(t *testing.T) {
	for _, tc := range []struct {
		name, finalizer string
		object          func() client.Object
		validate        func(client.Object, client.Object) error
	}{
		{"NUTServer", "power.zalud.io/nutserver-cleanup", func() client.Object { return &power.NUTServer{} }, func(old, next client.Object) error {
			_, err := (&NUTServerCustomValidator{}).ValidateUpdate(context.Background(), old.(*power.NUTServer), next.(*power.NUTServer))
			return err
		}},
		{"NodePowerAgent", "power.zalud.io/nodepoweragent-cleanup", func() client.Object { return &power.NodePowerAgent{} }, func(old, next client.Object) error {
			_, err := (&NodePowerAgentCustomValidator{}).ValidateUpdate(context.Background(), old.(*power.NodePowerAgent), next.(*power.NodePowerAgent))
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			old := tc.object()
			old.SetName("invalid-legacy")
			old.SetFinalizers([]string{tc.finalizer, "example.org/foreign"})
			raw, err := json.Marshal(old)
			if err != nil {
				t.Fatal(err)
			}
			ctx := admission.NewContextWithRequest(context.Background(), admission.Request{AdmissionRequest: admissionv1.AdmissionRequest{
				Operation: admissionv1.Update,
				OldObject: runtime.RawExtension{Raw: raw},
			}})
			defaultObject := func(obj client.Object) {
				t.Helper()
				var err error
				switch obj := obj.(type) {
				case *power.NUTServer:
					err = (&NUTServerCustomDefaulter{}).Default(ctx, obj)
				case *power.NodePowerAgent:
					err = (&NodePowerAgentCustomDefaulter{}).Default(ctx, obj)
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := tc.validate(old, old.DeepCopyObject().(client.Object)); err == nil {
				t.Fatal("fixture must be rejected by normal validation")
			}
			next := old.DeepCopyObject().(client.Object)
			next.SetFinalizers([]string{"example.org/foreign"})
			before := next.DeepCopyObject()
			defaultObject(next)
			if !equality.Semantic.DeepEqual(before, next) {
				t.Error("defaulting changed exact legacy-finalizer removal with missing defaults")
			}
			if err := tc.validate(old, next); err != nil {
				t.Errorf("exact legacy cleanup should pass despite invalid spec: %v", err)
			}
			for _, mutate := range []func(client.Object){
				func(obj client.Object) { obj.SetFinalizers(nil) },
				func(obj client.Object) { obj.SetFinalizers(append(obj.GetFinalizers(), "example.org/new")) },
				func(obj client.Object) { obj.SetAnnotations(map[string]string{"approved": "true"}) },
				func(obj client.Object) { obj.SetLabels(map[string]string{"changed": "true"}) },
				func(obj client.Object) {
					switch obj := obj.(type) {
					case *power.NUTServer:
						obj.Spec.Namespace = "changed"
					case *power.NodePowerAgent:
						obj.Spec.Namespace = "changed"
					}
				},
			} {
				changed := next.DeepCopyObject().(client.Object)
				mutate(changed)
				defaultObject(changed)
				switch obj := changed.(type) {
				case *power.NUTServer:
					if obj.Spec.Placement.PriorityClassName != defaultNUTServerPriorityClassName {
						t.Error("mixed change skipped normal NUTServer defaults")
					}
				case *power.NodePowerAgent:
					if obj.Spec.Placement.PriorityClassName != defaultNodePowerAgentPriorityClassName {
						t.Error("mixed change skipped normal NodePowerAgent defaults")
					}
				}
				if err := tc.validate(old, changed); err == nil {
					t.Fatal("cleanup bypassed validation for another change")
				}
			}
			t.Run("ordinary update", func(t *testing.T) {
				unchanged := old.DeepCopyObject().(client.Object)
				defaultObject(unchanged)
				if equality.Semantic.DeepEqual(old, unchanged) {
					t.Error("ordinary update skipped defaults")
				}
				if err := tc.validate(old, unchanged); err == nil {
					t.Error("ordinary update bypassed validation")
				}
			})
			for _, request := range []struct {
				name string
				req  *admissionv1.AdmissionRequest
			}{
				{name: "no request"},
				{name: "create", req: &admissionv1.AdmissionRequest{Operation: admissionv1.Create, OldObject: runtime.RawExtension{Raw: raw}}},
				{name: "missing old object", req: &admissionv1.AdmissionRequest{Operation: admissionv1.Update}},
				{name: "malformed old object", req: &admissionv1.AdmissionRequest{Operation: admissionv1.Update, OldObject: runtime.RawExtension{Raw: []byte("{")}}},
			} {
				t.Run(request.name, func(t *testing.T) {
					ctx = context.Background()
					if request.req != nil {
						ctx = admission.NewContextWithRequest(ctx, admission.Request{AdmissionRequest: *request.req})
					}
					obj := next.DeepCopyObject().(client.Object)
					defaultObject(obj)
					if equality.Semantic.DeepEqual(next, obj) {
						t.Error("request without an exact Update removal skipped defaults")
					}
				})
			}
		})
	}
}
