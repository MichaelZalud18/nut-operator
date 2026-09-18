//go:build e2e

// Copyright 2026 Michael Zalud.
// SPDX-License-Identifier: Apache-2.0

package e2e

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/onsi/ginkgo/v2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestLogicalFlowCleanupReportsSecondaryFailure(t *testing.T) {
	var diagnostics bytes.Buffer
	ginkgo.GinkgoWriter.TeeTo(&diagnostics)
	t.Cleanup(ginkgo.GinkgoWriter.ClearTeeWriters)
	failure := errors.New("timed out waiting for the condition on nodepoweragents/test2-agent")
	cleanup := logicalFlowCleanupTestFixture()
	// A real manifest contains credentials; diagnostics must identify its index,
	// never dump stdin or successful command output.
	cleanup.manifests[1] = logicalFlowStorage()
	err := cleanup.run(func(cmd *exec.Cmd) (string, error) {
		key := logicalFlowCleanupCommandKey(t, cmd)
		if key == "delete:"+cleanup.manifests[1] {
			return "", failure
		}
		if strings.HasPrefix(key, "delete:") {
			return "successful-output-not-for-diagnostics", nil
		}
		return "", nil
	})
	if !errors.Is(err, failure) {
		t.Fatalf("lost cleanup failure: %v", err)
	}
	for _, want := range []string{"delete manifest[1]", failure.Error()} {
		if !strings.Contains(diagnostics.String(), want) {
			t.Fatalf("missing %q in diagnostics: %s", want, &diagnostics)
		}
	}
	for _, forbidden := range []string{"stringData:", "postgres://", "test2-fixture", "successful-output-not-for-diagnostics"} {
		if strings.Contains(diagnostics.String(), forbidden) {
			t.Fatalf("diagnostics exposed %q", forbidden)
		}
	}
}

func TestLogicalFlowCleanupReportsSurvivingResource(t *testing.T) {
	var diagnostics bytes.Buffer
	ginkgo.GinkgoWriter.TeeTo(&diagnostics)
	t.Cleanup(ginkgo.GinkgoWriter.ClearTeeWriters)
	err := logicalFlowCleanupTestFixture().run(func(cmd *exec.Cmd) (string, error) {
		if logicalFlowCleanupCommandKey(t, cmd) == "get:operands" {
			return "namespace/operands\n", nil
		}
		return "", nil
	})
	if err == nil || !strings.Contains(diagnostics.String(), "get namespace/operands") ||
		!strings.Contains(diagnostics.String(), "resources remain: namespace/operands") {
		t.Fatalf("missing surviving-resource diagnostic: error=%v, diagnostics=%s", err, &diagnostics)
	}
}

func TestLogicalFlowCleanupBoundsDiagnostics(t *testing.T) {
	var diagnostics bytes.Buffer
	ginkgo.GinkgoWriter.TeeTo(&diagnostics)
	t.Cleanup(ginkgo.GinkgoWriter.ClearTeeWriters)
	failure := errors.New(strings.Repeat("x", 9000))
	err := logicalFlowCleanupReportError("delete manifest[1]", failure)
	if !errors.Is(err, failure) || !strings.Contains(err.Error(), failure.Error()) {
		t.Fatal("diagnostic truncation changed the returned error")
	}
	want := "TEST-2 cleanup failed: " + err.Error()[:8192] + " [truncated]\n"
	if diagnostics.String() != want {
		t.Fatal("oversized diagnostic was not bounded")
	}
}

func TestLogicalFlowCleanupFailedGetIsNotSurvivorEvidence(t *testing.T) {
	failure := errors.New("API unavailable")
	err := logicalFlowCleanupTestFixture().run(func(cmd *exec.Cmd) (string, error) {
		if logicalFlowCleanupCommandKey(t, cmd) == "get:operands" {
			return "error: API unavailable", failure
		}
		return "", nil
	})
	if !errors.Is(err, failure) || strings.Contains(err.Error(), "resources remain") {
		t.Fatalf("failed get must retain its error without claiming survivors: %v", err)
	}
}

func logicalFlowCleanupTestFixture() *logicalFlowCleanup {
	return &logicalFlowCleanup{
		target: "worker-a", manifests: []string{"storage", "stack", "flow"},
		namespaces: []string{"operands", "workloads"},
		originals: []appsv1.Deployment{
			{ObjectMeta: metav1.ObjectMeta{Name: "dns", Namespace: "kube-system", UID: "dns-uid"}},
			{ObjectMeta: metav1.ObjectMeta{Name: "cert", Namespace: "cert-manager", UID: "cert-uid"}},
		},
	}
}

func logicalFlowCleanupCommandKey(t *testing.T, cmd *exec.Cmd) string {
	t.Helper()
	if cmd.Cancel == nil || cmd.WaitDelay <= 0 || cmd.Args[1] != "--request-timeout=15s" {
		t.Fatalf("unbounded cleanup command: %v", cmd.Args)
	}
	action := cmd.Args[2]
	if action == "delete" {
		for _, flag := range []string{"--ignore-not-found=true", "--wait=true", "--timeout=1m", "--cascade=foreground"} {
			if !slices.Contains(cmd.Args, flag) {
				t.Fatalf("missing deletion guard %s: %v", flag, cmd.Args)
			}
		}
	}
	if cmd.Stdin != nil {
		data, err := io.ReadAll(cmd.Stdin)
		if err != nil {
			t.Fatal(err)
		}
		return action + ":" + string(data)
	}
	switch action {
	case "get", "delete":
		return action + ":" + cmd.Args[len(cmd.Args)-1]
	case "patch", "rollout":
		return action + ":" + strings.TrimPrefix(cmd.Args[4], "deployment/")
	default:
		return action
	}
}

func TestLogicalFlowCleanupOrdering(t *testing.T) {
	var calls []string
	err := logicalFlowCleanupTestFixture().run(func(cmd *exec.Cmd) (string, error) {
		calls = append(calls, logicalFlowCleanupCommandKey(t, cmd))
		return "", nil
	})
	want := []string{
		"delete:flow", "delete:stack", "delete:storage", "delete:workloads", "delete:operands",
		"get:flow", "get:stack", "get:storage", "get:workloads", "get:operands",
		"uncordon", "taint", "patch:dns", "rollout:dns", "patch:cert", "rollout:cert",
	}
	if err != nil || !reflect.DeepEqual(calls, want) {
		t.Fatalf("cleanup calls = %v, want %v; error: %v", calls, want, err)
	}
}

func TestLogicalFlowCleanupFailureKeepsReservation(t *testing.T) {
	for failAt := 0; failAt < 10; failAt++ {
		t.Run(fmt.Sprintf("command-%d", failAt), func(t *testing.T) {
			failure := errors.New("cleanup API failure")
			var calls []string
			err := logicalFlowCleanupTestFixture().run(func(cmd *exec.Cmd) (string, error) {
				calls = append(calls, logicalFlowCleanupCommandKey(t, cmd))
				if len(calls)-1 == failAt {
					return "", failure
				}
				return "", nil
			})
			if !errors.Is(err, failure) || len(calls) != 10 || !strings.Contains(err.Error(), "retaining reservation") {
				t.Fatalf("cleanup must attempt every deletion/check and skip restoration: calls=%v, error=%v", calls, err)
			}
		})
	}
}

func TestLogicalFlowCleanupSurvivorKeepsReservation(t *testing.T) {
	for _, survivor := range []string{"flow", "stack", "storage", "workloads", "operands"} {
		t.Run(survivor, func(t *testing.T) {
			var calls []string
			err := logicalFlowCleanupTestFixture().run(func(cmd *exec.Cmd) (string, error) {
				key := logicalFlowCleanupCommandKey(t, cmd)
				calls = append(calls, key)
				if key == "get:"+survivor {
					return "surviving-resource\n", nil
				}
				return "", nil
			})
			if err == nil || !strings.Contains(err.Error(), "resources remain") || len(calls) != 10 {
				t.Fatalf("survivor must prevent restoration: calls=%v, error=%v", calls, err)
			}
		})
	}
}

func TestLogicalFlowCleanupAttemptsAllRestorations(t *testing.T) {
	failures := []error{errors.New("uncordon failed"), errors.New("taint removal failed"), errors.New("patch failed"), errors.New("rollout failed")}
	var calls []string
	err := logicalFlowCleanupTestFixture().run(func(cmd *exec.Cmd) (string, error) {
		calls = append(calls, logicalFlowCleanupCommandKey(t, cmd))
		if len(calls) > 10 {
			return "", failures[(len(calls)-11)%len(failures)]
		}
		return "", nil
	})
	if len(calls) != 16 {
		t.Fatalf("restoration stopped early: %v", calls)
	}
	for _, failure := range failures {
		if !errors.Is(err, failure) {
			t.Fatalf("lost restoration error %v: %v", failure, err)
		}
	}
}

func TestLogicalFlowPlacementPatchRestoresSelectors(t *testing.T) {
	for _, selectors := range []map[string]string{nil, {"kubernetes.io/os": "linux"}, {"kubernetes.io/os": "linux", "kubernetes.io/hostname": "worker-a"}} {
		deployment := appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Name: "dns", Namespace: "kube-system", UID: "original-uid"},
			Spec:       appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{NodeSelector: selectors}}},
		}
		original := deployment.DeepCopy()
		live, err := json.Marshal(deployment)
		if err != nil {
			t.Fatal(err)
		}
		run := func(cmd *exec.Cmd) (string, error) {
			logicalFlowCleanupCommandKey(t, cmd)
			index := slices.Index(cmd.Args, "-p")
			if index < 0 || !slices.Contains(cmd.Args, "--type=merge") {
				t.Fatalf("unexpected patch: %v", cmd.Args)
			}
			var patch appsv1.Deployment
			if err := json.Unmarshal([]byte(cmd.Args[index+1]), &patch); err != nil || patch.UID != deployment.UID {
				t.Fatalf("missing UID guard: %v, %v", patch.UID, err)
			}
			var mergeErr error
			live, mergeErr = jsonpatch.MergePatch(live, []byte(cmd.Args[index+1]))
			return "", mergeErr
		}
		if err := logicalFlowPatchPlacement(run, deployment, "worker-b"); err != nil {
			t.Fatal(err)
		}
		var restore any
		if hostname, present := selectors["kubernetes.io/hostname"]; present {
			restore = hostname
		}
		if err := logicalFlowPatchPlacement(run, deployment, restore); err != nil {
			t.Fatal(err)
		}
		var restored appsv1.Deployment
		if err := json.Unmarshal(live, &restored); err != nil {
			t.Fatal(err)
		}
		// Kubernetes treats an absent and an empty nodeSelector as equivalent.
		if !reflect.DeepEqual(deployment, *original) || len(restored.Spec.Template.Spec.NodeSelector) != len(selectors) {
			t.Fatal("placement patch mutated the snapshot or failed to remove the added selector")
		}
		for key, value := range selectors {
			if restored.Spec.Template.Spec.NodeSelector[key] != value {
				t.Fatalf("selector %s not restored", key)
			}
		}
	}
}
