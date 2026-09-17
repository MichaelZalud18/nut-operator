//go:build e2e

// Copyright 2026 Michael Zalud.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"errors"
	"io"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"k8s.io/apimachinery/pkg/util/yaml"
)

func TestDummyUPSManifest(t *testing.T) {
	for _, fixture := range []struct{ namespace, server, ups, display string }{
		{"power-recovery-e2e", "recovery-e2e-nutserver", "recovery-e2e-ups", "Driver Recovery E2E Dummy UPS"},
		{"power-signal-e2e", "signal-e2e-nutserver", "signal-e2e-ups", "Signal Handoff E2E Dummy UPS"},
		{"power-fanout-e2e", "fanout-e2e-nutserver", "fanout-e2e-ups", "Fanout E2E Dummy UPS"},
	} {
		t.Run(fixture.namespace, func(t *testing.T) {
			decoder := yaml.NewYAMLOrJSONDecoder(strings.NewReader(dummyUPSManifest(
				fixture.namespace, fixture.server, fixture.ups, fixture.display)), 4096)
			var ups power.UPSDevice
			if err := decoder.Decode(&ups); err != nil {
				t.Fatal(err)
			}
			if ups.Kind != "UPSDevice" || ups.Name != fixture.ups || ups.Spec.DisplayName != fixture.display || ups.Spec.Driver != "dummy-ups" {
				t.Fatalf("wrong UPS fixture: %+v", ups)
			}
			var server power.NUTServer
			if err := decoder.Decode(&server); err != nil {
				t.Fatal(err)
			}
			if server.Kind != "NUTServer" || server.Name != fixture.server || server.Spec.Namespace != fixture.namespace {
				t.Fatalf("wrong NUTServer identity: %+v", server)
			}
			if len(server.Spec.DeviceRefs) != 1 || server.Spec.DeviceRefs[0].Name != ups.Name {
				t.Fatalf("server does not reference its own UPS: %+v", server.Spec.DeviceRefs)
			}
			if server.Spec.Image.Repository != nutServerRepository || server.Spec.Image.Tag != operandImageTag ||
				string(server.Spec.Image.PullPolicy) != "IfNotPresent" {
				t.Fatalf("fixture lost the suite operand image: %+v", server.Spec.Image)
			}
			if string(server.Spec.Auth.Mode) != "OperatorManaged" || string(server.Spec.TLS.Mode) != "Disabled" {
				t.Fatalf("fixture auth/TLS changed: %+v", server.Spec)
			}
			var extra any
			if err := decoder.Decode(&extra); err != io.EOF {
				t.Fatalf("unexpected extra document: %v, %v", extra, err)
			}
		})
	}
}

func TestReadyPodNames(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		want         []string
	}{
		{"empty", "", nil},
		{"pending", "pending\tFalse\nstarting\t\nunknown\tUnknown\n", nil},
		{"replacement", "old\tFalse\nnew\tTrue\n", []string{"new"}},
		{"duplicates remain visible", "first\tTrue\n\nsecond\tTrue\n", []string{"first", "second"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := readyPodNames(tc.output); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("ready pods = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCleanupFixture(t *testing.T) {
	const manifest = "kind: UPSDevice\nmetadata:\n  name: partial-fixture\n---\nkind: NUTServer\n"
	for _, tc := range []struct {
		name, manifest, namespace string
		wantCalls                 int
	}{
		{"partial apply", manifest, "fixture-ns", 2},
		{"manifest only", manifest, "", 1},
		{"namespace only", "", "fixture-ns", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls [][]string
			run := func(cmd *exec.Cmd) (string, error) {
				calls = append(calls, cmd.Args)
				if len(calls) == 1 && tc.manifest != "" {
					data, err := io.ReadAll(cmd.Stdin)
					if err != nil || string(data) != tc.manifest {
						t.Fatalf("cleanup changed the applied manifest: %q, %v", data, err)
					}
					want := []string{"kubectl", "delete", "-f", "-", "--ignore-not-found", "--wait=false"}
					if !reflect.DeepEqual(cmd.Args, want) {
						t.Fatalf("manifest cleanup = %v", cmd.Args)
					}
				} else {
					want := []string{"kubectl", "delete", "ns", tc.namespace, "--ignore-not-found", "--wait=false"}
					if !reflect.DeepEqual(cmd.Args, want) {
						t.Fatalf("namespace cleanup = %v", cmd.Args)
					}
				}
				return "", errors.New("simulated deletion failure")
			}
			cleanupFixture(run, tc.manifest, tc.namespace)
			if len(calls) != tc.wantCalls {
				t.Fatalf("cleanup stopped after %d calls, want %d", len(calls), tc.wantCalls)
			}
		})
	}
}
