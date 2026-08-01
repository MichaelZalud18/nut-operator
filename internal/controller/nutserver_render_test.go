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
	})
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
	})
	if err != nil {
		t.Fatalf("renderUPSConf returned error: %v", err)
	}

	if !strings.Contains(conf, "  authconf = /etc/nut/upstream-auth/rack-a-ups.nutauth.conf") {
		t.Fatalf("rendered upstream NUT config missing Secret authconf path:\n%s", conf)
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
