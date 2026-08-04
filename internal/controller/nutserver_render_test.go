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
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

func TestRenderUPSConfRendersUpstreamNUTRepeater(t *testing.T) {
	port := int32(3493)
	strictStart := false
	conf, err := renderUPSConf([]powerv1alpha1.UPSDevice{
		{
			Spec: powerv1alpha1.UPSDeviceSpec{
				UpstreamNUT: &powerv1alpha1.UPSUpstreamNUTSpec{
					Host:        "ups-tower.example.net",
					Port:        &port,
					UPSName:     "ups",
					StrictStart: &strictStart,
				},
				DriverOptions: map[string]string{
					"desc": "ubiquiti tower",
				},
			},
			Status: powerv1alpha1.UPSDeviceStatus{
				NUTName: "tower-ups",
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("renderUPSConf returned error: %v", err)
	}

	for _, want := range []string{
		"[tower-ups]",
		"  driver = dummy-ups",
		"  port = ups@ups-tower.example.net:3493",
		"  mode = repeater",
		"  authconf = none",
		"  repeater_disable_strict_start = true",
		"  desc = ubiquiti tower",
	} {
		if !strings.Contains(conf, want) {
			t.Fatalf("rendered upstream NUT config missing %q:\n%s", want, conf)
		}
	}
}

func TestRenderUPSConfRendersUpstreamNUTSecretAuthPath(t *testing.T) {
	conf, err := renderUPSConf([]powerv1alpha1.UPSDevice{
		{
			ObjectMeta: objectMeta("rack-a-ups"),
			Spec: powerv1alpha1.UPSDeviceSpec{
				UpstreamNUT: &powerv1alpha1.UPSUpstreamNUTSpec{
					Host:    "ups-2u.example.net",
					UPSName: "ups",
					Auth: powerv1alpha1.UPSUpstreamNUTAuthSpec{
						Mode: powerv1alpha1.UPSUpstreamNUTAuthSecret,
						SecretKeyRef: &powerv1alpha1.SecretKeyReference{
							Namespace: "power-system",
							Name:      "ups-2u-auth",
							Key:       "nutauth.conf",
						},
					},
				},
			},
		},
	}, nil)
	if err != nil {
		t.Fatalf("renderUPSConf returned error: %v", err)
	}

	if !strings.Contains(conf, "  authconf = /etc/nut/upstream-auth/rack-a-ups.nutauth.conf") {
		t.Fatalf("rendered upstream NUT config missing Secret authconf path:\n%s", conf)
	}
}

func TestUpsdReadinessProbeScriptQueriesRealData(t *testing.T) {
	script := upsdReadinessProbeScript(3493)
	for _, want := range []string{
		"upsc -l localhost:3493",
		`upsc "$u@localhost:3493" ups.status`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("readiness probe script missing %q:\n%s", want, script)
		}
	}
}

func TestRenderUPSConfMergesCredentialSecretOverDriverOptions(t *testing.T) {
	conf, err := renderUPSConf([]powerv1alpha1.UPSDevice{
		{
			ObjectMeta: objectMeta("rack-a-ups"),
			Spec: powerv1alpha1.UPSDeviceSpec{
				Driver: "snmp-ups",
				Endpoint: &powerv1alpha1.UPSEndpointSpec{
					Host: "192.168.1.209",
				},
				DriverOptions: map[string]string{
					"snmp_version": "v3",
					"secName":      "placeholder-overridden-by-secret",
				},
			},
		},
	}, map[string]map[string]string{
		"rack-a-ups": {
			"secName":      "real-username",
			"authPassword": "real-auth-pass",
			"privPassword": "real-priv-pass",
		},
	})
	if err != nil {
		t.Fatalf("renderUPSConf returned error: %v", err)
	}

	for _, want := range []string{
		"  snmp_version = v3",
		"  secName = real-username",
		"  authPassword = real-auth-pass",
		"  privPassword = real-priv-pass",
	} {
		if !strings.Contains(conf, want) {
			t.Fatalf("rendered ups.conf missing %q:\n%s", want, conf)
		}
	}
	if strings.Contains(conf, "placeholder-overridden-by-secret") {
		t.Fatalf("credentialSecretRef value did not override driverOptions collision:\n%s", conf)
	}
}

func TestResolveUPSDeviceCredentialsFetchesReferencedSecret(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ups-1-snmp", Namespace: "power-system"},
		Data: map[string][]byte{
			"secName":      []byte("real-username"),
			"authPassword": []byte("real-auth-pass"),
		},
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()

	credentials, err := resolveUPSDeviceCredentials(context.Background(), fakeClient, "power-system", []powerv1alpha1.UPSDevice{
		{
			ObjectMeta: objectMeta("ups-1"),
			Spec: powerv1alpha1.UPSDeviceSpec{
				CredentialSecretRef: &powerv1alpha1.NamespacedNameReference{
					Namespace: "power-system",
					Name:      "ups-1-snmp",
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("resolveUPSDeviceCredentials returned error: %v", err)
	}
	if got := credentials["ups-1"]["secName"]; got != "real-username" {
		t.Fatalf("secName = %q, want %q", got, "real-username")
	}
	if got := credentials["ups-1"]["authPassword"]; got != "real-auth-pass" {
		t.Fatalf("authPassword = %q, want %q", got, "real-auth-pass")
	}
}

func TestResolveUPSDeviceCredentialsRejectsCrossNamespaceRef(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	_, err := resolveUPSDeviceCredentials(context.Background(), fakeClient, "power-system", []powerv1alpha1.UPSDevice{
		{
			ObjectMeta: objectMeta("ups-1"),
			Spec: powerv1alpha1.UPSDeviceSpec{
				CredentialSecretRef: &powerv1alpha1.NamespacedNameReference{
					Namespace: "other-system",
					Name:      "ups-1-snmp",
				},
			},
		},
	})
	if err == nil {
		t.Fatal("expected cross-namespace credentialSecretRef to be rejected")
	}
}

func TestUpstreamNUTAuthProjectionsRequireOperandNamespace(t *testing.T) {
	_, err := upstreamNUTAuthProjections([]powerv1alpha1.UPSDevice{
		{
			ObjectMeta: objectMeta("rack-a-ups"),
			Spec: powerv1alpha1.UPSDeviceSpec{
				UpstreamNUT: &powerv1alpha1.UPSUpstreamNUTSpec{
					Host:    "ups-2u.example.net",
					UPSName: "ups",
					Auth: powerv1alpha1.UPSUpstreamNUTAuthSpec{
						Mode: powerv1alpha1.UPSUpstreamNUTAuthSecret,
						SecretKeyRef: &powerv1alpha1.SecretKeyReference{
							Namespace: "other-system",
							Name:      "ups-2u-auth",
							Key:       "nutauth.conf",
						},
					},
				},
			},
		},
	}, "power-system")
	if err == nil {
		t.Fatal("expected cross-namespace upstream NUT auth Secret to be rejected")
	}
}

func TestProbeUpstreamNUTDevicesUsesConfiguredProber(t *testing.T) {
	reconciler := &NUTServerReconciler{
		UpstreamProber: fakeUpstreamNUTProber{
			result: upstreamNUTProbeResult{Reachable: true, Message: "fake ok"},
		},
	}
	statuses := reconciler.probeUpstreamNUTDevices(context.Background(), []powerv1alpha1.UPSDevice{
		{
			ObjectMeta: objectMeta("rack-a-ups"),
			Spec: powerv1alpha1.UPSDeviceSpec{
				UpstreamNUT: &powerv1alpha1.UPSUpstreamNUTSpec{
					Host:    "ups-2u.example.net",
					UPSName: "ups",
				},
			},
		},
	})

	if len(statuses) != 1 {
		t.Fatalf("expected one upstream status, got %#v", statuses)
	}
	if statuses[0].Reachable == nil || !*statuses[0].Reachable {
		t.Fatalf("expected reachable upstream status, got %#v", statuses[0])
	}
	if statuses[0].Message != "fake ok" {
		t.Fatalf("expected fake probe message, got %q", statuses[0].Message)
	}
}

type fakeUpstreamNUTProber struct {
	result upstreamNUTProbeResult
}

func (p fakeUpstreamNUTProber) Probe(context.Context, upstreamNUTProbeTarget) upstreamNUTProbeResult {
	return p.result
}
