// Copyright 2026 Michael Zalud.
// SPDX-License-Identifier: Apache-2.0

package quickstart_test

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/kubeinventory"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
	"github.com/MichaelZalud18/nut-operator/internal/shutdownflow"
	webhooks "github.com/MichaelZalud18/nut-operator/internal/webhook/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/util/yaml"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

const example = "../../docs/examples/quickstart"

func render(t *testing.T, args ...string) ([]byte, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return exec.CommandContext(ctx, "python3", append([]string{filepath.Join(example, "render.py")}, args...)...).CombinedOutput()
}

// Read the shipped files, including the same node-binding renderer used by operators.
// Only Nodes and agent status are synthetic; no controller, API server or operand runs here.
func fixture(t *testing.T) (*runtime.Scheme, []client.Object, *power.ShutdownFlow) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := power.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	decode := serializer.NewCodecFactory(scheme).UniversalDeserializer()
	var objects []client.Object
	var flow *power.ShutdownFlow
	for _, file := range []string{"bootstrap.yaml", "ups-nut.yaml", "topology.yaml", "shutdown.yaml"} {
		var data []byte
		var err error
		if file == "topology.yaml" {
			data, err = render(t, "--control", "test-control", "--worker-a", "test-worker-a", "--worker-b", "test-worker-b")
		} else {
			data, err = os.ReadFile(filepath.Join(example, file))
		}
		if err != nil {
			t.Fatalf("%s: %v: %s", file, err, data)
		}
		reader := yaml.NewYAMLOrJSONDecoder(bytes.NewReader(data), 4096)
		for {
			var raw runtime.RawExtension
			if err := reader.Decode(&raw); err == io.EOF {
				break
			} else if err != nil {
				t.Fatal(err)
			}
			obj, _, err := decode.Decode(raw.Raw, nil, nil)
			if err != nil {
				t.Fatalf("%s: %v", file, err)
			}
			switch v := obj.(type) {
			case *power.ShutdownFlow:
				flow = v
				if v.Spec.Mode != "DryRun" {
					t.Fatal("example permits enforcement")
				}
			case *power.NodePowerAgent:
				if v.Spec.Mode != "DryRun" || v.Spec.Shutdown.ActuatorPolicy != "Simulate" {
					t.Fatal("example permits actuation")
				}
				if v.Name == "quickstart-control" {
					v.Status.SelectedNodes = []string{"test-control"}
				} else {
					v.Status.SelectedNodes = []string{"test-worker-a", "test-worker-b"}
				}
			}
			objects = append(objects, obj.(client.Object))
		}
	}
	for _, name := range []string{"test-control", "test-worker-a", "test-worker-b"} {
		role := "worker"
		if name == "test-control" {
			role = "control"
		}
		objects = append(objects, &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{"power.example.com/quickstart-role": role}}})
	}
	if flow == nil {
		t.Fatal("missing flow")
	}
	return scheme, objects, flow
}

func TestQuickstartPlan(t *testing.T) {
	scheme, objects, flow := fixture(t)
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	bundle, diagnostics, err := kubeinventory.ResolveStructuralBundle(context.Background(), c)
	if err != nil {
		t.Fatalf("resolve: %v: %+v", err, diagnostics)
	}
	if len(bundle.Topology.Domains) != 2 {
		t.Fatalf("domains: %+v", bundle.Topology.Domains)
	}
	wantNodes := map[string][]string{"quickstart-a": {"test-control", "test-worker-a"}, "quickstart-b": {"test-worker-b"}}
	for _, domain := range bundle.Topology.Domains {
		if !reflect.DeepEqual(domain.Nodes, wantNodes[domain.Name]) {
			t.Fatalf("wrong domain membership: %+v", domain)
		}
		t.Logf("domain: %+v", domain)
	}
	compiled := shutdownflow.CompileFlow(flow, bundle, power.PowerShutdownTierPolicySpec{})
	for _, d := range compiled.Diagnostics {
		if d.Severity == planner.DiagnosticError {
			t.Fatalf("compile: %+v", compiled.Diagnostics)
		}
	}
	if compiled.ConfigHash == "" || compiled.Artifact == nil || len(compiled.BlockedNodeReleases) != 0 {
		t.Fatalf("incomplete plan: %+v", compiled)
	}
	want := []struct {
		groups   []string
		tier     int32
		duration time.Duration
	}{
		{[]string{"drain-workers"}, 2, 2 * time.Minute},
		{[]string{"stop-workers"}, 2, time.Minute},
		{[]string{"stop-control"}, 1, time.Minute},
	}
	if len(compiled.Waves) != len(want) {
		t.Fatalf("waves: %+v", compiled.Waves)
	}
	for i, wave := range compiled.Waves {
		if !reflect.DeepEqual(wave.Groups, want[i].groups) || wave.ShutdownTier == nil || *wave.ShutdownTier != want[i].tier || wave.Duration == nil || wave.Duration.Duration != want[i].duration {
			t.Fatalf("wave %d: %+v", i, wave)
		}
		t.Logf("wave %d tier %d %s %v", wave.Index, *wave.ShutdownTier, wave.Duration.Duration, wave.Groups)
	}
	if compiled.EstimatedDuration.Duration != 4*time.Minute {
		t.Fatalf("estimate: %v", compiled.EstimatedDuration)
	}
	// Coverage must come from resolved agent status, not invented inventory host names.
	membership := shutdownflow.PlannerGroupNodes(flow, bundle)
	if len(membership) != 3 || len(membership[1].Releases) != 2 || len(membership[2].Releases) != 1 {
		t.Fatalf("target coverage: %+v", membership)
	}
	for _, group := range membership {
		if group.Unresolved {
			t.Fatalf("unresolved group: %+v", group)
		}
	}
}

func TestQuickstartAdmission(t *testing.T) {
	_, objects, _ := fixture(t)
	ctx := context.Background()
	for _, obj := range objects {
		t.Run(obj.GetObjectKind().GroupVersionKind().Kind+"/"+obj.GetName(), func(t *testing.T) {
			var err error
			switch v := obj.(type) {
			case *power.PowerManagementCluster:
				if err = (&webhooks.PowerManagementClusterCustomDefaulter{}).Default(ctx, v); err == nil {
					_, err = (&webhooks.PowerManagementClusterCustomValidator{}).ValidateCreate(ctx, v)
				}
			case *power.UPSDevice:
				if err = (&webhooks.UPSDeviceCustomDefaulter{}).Default(ctx, v); err == nil {
					_, err = (&webhooks.UPSDeviceCustomValidator{}).ValidateCreate(ctx, v)
				}
			case *power.UPSCapabilityProfile:
				if err = (&webhooks.UPSCapabilityProfileCustomDefaulter{}).Default(ctx, v); err == nil {
					_, err = (&webhooks.UPSCapabilityProfileCustomValidator{}).ValidateCreate(ctx, v)
				}
			case *power.NUTServer:
				if err = (&webhooks.NUTServerCustomDefaulter{}).Default(ctx, v); err == nil {
					_, err = (&webhooks.NUTServerCustomValidator{}).ValidateCreate(ctx, v)
				}
			case *power.PowerInventoryNode:
				if err = (&webhooks.PowerInventoryNodeCustomDefaulter{}).Default(ctx, v); err == nil {
					_, err = (&webhooks.PowerInventoryNodeCustomValidator{}).ValidateCreate(ctx, v)
				}
			case *power.PowerInventoryEdge:
				if err = (&webhooks.PowerInventoryEdgeCustomDefaulter{}).Default(ctx, v); err == nil {
					_, err = (&webhooks.PowerInventoryEdgeCustomValidator{}).ValidateCreate(ctx, v)
				}
			case *power.PowerInfrastructure:
				if err = (&webhooks.PowerInfrastructureCustomDefaulter{}).Default(ctx, v); err == nil {
					_, err = (&webhooks.PowerInfrastructureCustomValidator{}).ValidateCreate(ctx, v)
				}
			case *power.NodePowerAgent:
				if err = (&webhooks.NodePowerAgentCustomDefaulter{}).Default(ctx, v); err == nil {
					_, err = (&webhooks.NodePowerAgentCustomValidator{}).ValidateCreate(ctx, v)
				}
			case *power.ShutdownFlow:
				if err = (&webhooks.ShutdownFlowCustomDefaulter{}).Default(ctx, v); err == nil {
					_, err = (&webhooks.ShutdownFlowCustomValidator{}).ValidateCreate(ctx, v)
				}
			}
			if err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestQuickstartRejectsBrokenFeed(t *testing.T) {
	scheme, objects, _ := fixture(t)
	for _, obj := range objects {
		if edge, ok := obj.(*power.PowerInventoryEdge); ok && edge.Name == "quickstart-b-worker" {
			edge.Spec.Input = ""
		}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	_, diagnostics, err := kubeinventory.ResolveStructuralBundle(context.Background(), c)
	if err == nil {
		t.Fatalf("missing feed input accepted: %+v", diagnostics)
	}
}

func TestQuickstartNeedsRuntimeProfile(t *testing.T) {
	scheme, objects, flow := fixture(t)
	for _, obj := range objects {
		if profile, ok := obj.(*power.UPSCapabilityProfile); ok {
			profile.Spec.Telemetry.Variables = []string{"ups.status"}
		}
	}
	c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
	bundle, diagnostics, err := kubeinventory.ResolveStructuralBundle(context.Background(), c)
	if err != nil {
		t.Fatalf("resolve: %v: %+v", err, diagnostics)
	}
	compiled := shutdownflow.CompileFlow(flow, bundle, power.PowerShutdownTierPolicySpec{})
	if compiled.ConfigHash != "" {
		t.Fatal("runtime trigger compiled without runtime capability")
	}
	for _, d := range compiled.Diagnostics {
		if d.Reason == "TriggerUnsupportedByAllDevices" {
			return
		}
	}
	t.Fatalf("missing capability refusal: %+v", compiled.Diagnostics)
}

func TestQuickstartRendererRejectsInvalidBindings(t *testing.T) {
	for _, worker := range []string{"test-control", "QUICKSTART_WORKER_A", "bad/name", "a..b", "a.-b", "a-.b", ".a", "a.", strings.Repeat("a", 254)} {
		out, err := render(t, "--control", "test-control", "--worker-a", worker, "--worker-b", "test-worker-b")
		if err == nil || !strings.Contains(string(out), "distinct Kubernetes Node names") {
			t.Fatalf("%q: %v: %s", worker, err, out)
		}
	}
}

func TestQuickstartRendererAcceptsDNSSubdomains(t *testing.T) {
	for _, worker := range []string{"a", "worker-a.example.test", "1.worker-2"} {
		out, err := render(t, "--control", "test-control", "--worker-a", worker, "--worker-b", "test-worker-b")
		if err != nil || !strings.Contains(string(out), "nodeName: "+worker) {
			t.Fatalf("%q: %v: %s", worker, err, out)
		}
	}
}
