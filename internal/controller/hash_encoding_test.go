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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
)

func TestControllerHashEncodingDoesNotPanic(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func() (string, error)
	}{
		{"profile helper", func() (string, error) { return hashJSON(make(chan int)) }},
		{"hook raw object", func() (string, error) {
			return stableHookSpecHash(power.ShutdownHookSpec{Invocation: power.ShutdownHookInvocationSpec{
				KubernetesObject: &power.ShutdownHookKubernetesObjectSpec{
					Object: runtime.RawExtension{Raw: []byte(`{"broken":`)},
				},
			}})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			defer func() {
				if recovered := recover(); recovered != nil {
					t.Fatalf("hash encoding panicked: %v", recovered)
				}
			}()
			hash, err := tc.run()
			if hash != "" || err == nil || errors.Unwrap(err) == nil {
				t.Fatalf("expected empty hash and wrapped error, got %q, %v", hash, err)
			}
		})
	}
}

func TestHookDigestReturnsEncodingError(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := power.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	// Return malformed in-memory raw data without asking the fake API to serialize it.
	// Kubernetes admission is not claimed to accept this malformed object.
	kube := fake.NewClientBuilder().WithScheme(scheme).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(_ context.Context, _ client.WithWatch, key client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
			hook := obj.(*power.ShutdownHook)
			hook.Name, hook.Namespace = key.Name, key.Namespace
			if key.Name == "a-valid" {
				hook.Spec.Invocation.HTTP = &power.ShutdownHookHTTPSpec{URL: "https://example.invalid/hook"}
				return nil
			}
			hook.Spec.Invocation.KubernetesObject = &power.ShutdownHookKubernetesObjectSpec{
				Object: runtime.RawExtension{Raw: []byte(`{"broken":`)},
			}
			return nil
		},
	}).Build()
	r := ShutdownFlowReconciler{Client: kube}
	flow := &power.ShutdownFlow{}
	flow.Spec.Groups = []power.ShutdownGroup{
		{Action: power.ShutdownStepRunHook, HookRef: &power.NamespacedNameReference{Namespace: "hooks", Name: "a-valid"}},
		{Action: power.ShutdownStepRunHook, HookRef: &power.NamespacedNameReference{Namespace: "hooks", Name: "invalid"}},
	}
	digests, _, err := r.shutdownFlowHookDigests(context.Background(), flow, nil)
	var marshalErr *json.MarshalerError
	if len(digests) != 0 || !errors.As(err, &marshalErr) || !strings.Contains(err.Error(), "hooks/invalid") {
		t.Fatalf("expected contextual encoding error without partial digests, got %#v, %v", digests, err)
	}
}

func TestControllerHashEncodingCompatibility(t *testing.T) {
	for _, value := range []any{
		capabilityProfileFromUPSCapabilityProfile(&power.UPSCapabilityProfile{}),
		pduCapabilityProfileFromCRD(&power.PDUCapabilityProfile{}),
		power.ShutdownHookSpec{Invocation: power.ShutdownHookInvocationSpec{
			KubernetesObject: &power.ShutdownHookKubernetesObjectSpec{
				Object: runtime.RawExtension{Raw: []byte(`{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"example"}}`)},
			},
		}},
	} {
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(encoded)
		want := hex.EncodeToString(sum[:])
		got, err := hashJSON(value)
		if err != nil || got != want {
			t.Fatalf("hash encoding changed for %T: got %q, %v; want %q", value, got, err, want)
		}
		if spec, ok := value.(power.ShutdownHookSpec); ok {
			got, err := stableHookSpecHash(spec)
			if err != nil || got != want {
				t.Fatalf("hook hash encoding changed: got %q, %v; want %q", got, err, want)
			}
		}
	}
}
