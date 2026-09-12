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
	"testing"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/nut"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestTelemetryTargetCarriesTLSAndReloadsTrust(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = power.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	server := tlsEnabledNUTServer()
	server.Spec.Namespace = "power-system"
	server.Spec.DeviceRefs = []power.ObjectNameReference{{Name: "ups"}}
	server.Spec.TLS.ServerCARef = &power.NamespacedNameReference{Name: "trust"}
	server.Status = power.NUTServerStatus{ObservedGeneration: server.Generation, Phase: power.NUTServerPhaseReady,
		Conditions:       []metav1.Condition{{Type: power.ConditionReady, Status: metav1.ConditionTrue}},
		ServiceEndpoints: []power.ServiceEndpointStatus{{DNSName: "nut.example", Port: 3493}},
	}
	ca := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "trust", Namespace: "power-system"}, Data: map[string][]byte{"ca.crt": []byte("first-ca")}}
	device := &power.UPSDevice{ObjectMeta: metav1.ObjectMeta{Name: "ups"}}
	device.Spec.Driver = "dummy-ups"
	device.Status.Phase = power.UPSDevicePhaseOnline
	device.Status.LastPollTime = &metav1.Time{Time: time.Now()}
	device.Status.Conditions = []metav1.Condition{{Type: power.ConditionReady, Status: metav1.ConditionTrue}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(device).WithObjects(server, ca, device).Build()
	r := &UPSDeviceReconciler{Client: c}
	check := func(want string) {
		t.Helper()
		target, resolution, err := r.resolveTelemetryTarget(context.Background(), device)
		if err != nil || !resolution.Found {
			t.Fatalf("resolution=%+v error=%v", resolution, err)
		}
		if target.TLS.Mode != nut.TLSRequired || target.TLS.ServerName != "nut.example" || string(target.TLS.CABundle) != want {
			t.Fatalf("TLS target=%+v", target.TLS)
		}
	}
	check("first-ca")
	ca.Data["ca.crt"] = []byte("rotated-ca")
	if err := c.Update(context.Background(), ca); err != nil {
		t.Fatal(err)
	}
	check("rotated-ca")
	if err := c.Delete(context.Background(), ca); err != nil {
		t.Fatal(err)
	}
	if _, resolution, err := r.resolveTelemetryTarget(context.Background(), device); err != nil || resolution.Found || resolution.Reason != "TelemetryTLSInvalid" {
		t.Fatalf("missing trust resolution=%+v error=%v", resolution, err)
	}
	result, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Name: device.Name}})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Get(context.Background(), types.NamespacedName{Name: device.Name}, device); err != nil {
		t.Fatal(err)
	}
	ready := meta.FindStatusCondition(device.Status.Conditions, power.ConditionReady)
	if device.Status.Phase != power.UPSDevicePhaseUnknown || ready == nil || ready.Status != metav1.ConditionFalse || result.RequeueAfter <= 0 {
		t.Fatalf("TLS trust loss retained ready telemetry: %+v result=%+v", device.Status, result)
	}
}

func TestTelemetryTLSTrustBoundaries(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = power.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	for _, tc := range []struct {
		name   string
		mutate func(*power.NUTServer)
		want   string
		mode   nut.TLSMode
		fail   bool
	}{
		{name: "certificate anchor", want: "public-leaf", mode: nut.TLSRequired},
		{name: "disabled", mutate: func(s *power.NUTServer) { s.Spec.TLS.Mode = power.NUTTLSDisabled }, mode: nut.TLSDisabled},
		{name: "no certificate", mutate: func(s *power.NUTServer) { s.Spec.TLS.ServerCertificateRef = nil }, mode: nut.TLSDisabled},
		{name: "opportunistic", mutate: func(s *power.NUTServer) { s.Spec.TLS.Mode = power.NUTTLSOpportunistic }, want: "public-leaf", mode: nut.TLSOpportunistic},
		{name: "cross namespace", mutate: func(s *power.NUTServer) { s.Spec.TLS.ServerCertificateRef.Namespace = "elsewhere" }, fail: true},
		{name: "missing CA", mutate: func(s *power.NUTServer) { s.Spec.TLS.ServerCARef = &power.NamespacedNameReference{Name: "absent"} }, fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := tlsEnabledNUTServer()
			server.Spec.TLS.ServerCARef = nil
			server.Spec.Namespace = "power-system"
			if tc.mutate != nil {
				tc.mutate(server)
			}
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "server-tls", Namespace: "power-system"}, Data: map[string][]byte{"tls.crt": []byte("public-leaf"), "tls.key": []byte("never-copy-this")}}
			if server.Spec.TLS.ServerCertificateRef != nil {
				secret.Name = server.Spec.TLS.ServerCertificateRef.Name
			}
			c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
			got, err := (&UPSDeviceReconciler{Client: c}).telemetryTLS(context.Background(), server, "nut.example")
			if tc.fail {
				if err == nil {
					t.Fatal("expected rejection")
				}
				return
			}
			if err != nil || got.Mode != tc.mode || string(got.CABundle) != tc.want {
				t.Fatalf("TLS=%+v error=%v", got, err)
			}
		})
	}
}

func TestTelemetryTLSUsesManagementClusterNamespace(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = power.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)
	server := tlsEnabledNUTServer()
	server.Spec.Namespace = ""
	server.Spec.TLS.ServerCARef = nil
	server.Spec.TLS.ServerCertificateRef.Namespace = ""
	server.Spec.ManagementClusterRef = &power.ObjectNameReference{Name: "cluster"}
	cluster := &power.PowerManagementCluster{ObjectMeta: metav1.ObjectMeta{Name: "cluster"}}
	cluster.Spec.OperandNamespace = &power.OperandNamespaceSpec{Name: "tenant"}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: server.Spec.TLS.ServerCertificateRef.Name, Namespace: "tenant"}, Data: map[string][]byte{"tls.crt": []byte("tenant-leaf")}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(cluster, secret).Build()
	options, err := (&UPSDeviceReconciler{Client: c}).telemetryTLS(context.Background(), server, "nut.tenant.svc")
	if err != nil || string(options.CABundle) != "tenant-leaf" {
		t.Fatalf("options=%+v error=%v", options, err)
	}
}
