//go:build e2e

package e2e

import (
	"errors"
	"os/exec"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestLogicalFlowGetReplacesSnapshot(t *testing.T) {
	var node corev1.Node
	response := `{"metadata":{"name":"target","labels":{"old":"label"}},"spec":{"unschedulable":true,"taints":[{"key":"old","effect":"NoSchedule"}]}}`
	run := func(cmd *exec.Cmd) (string, error) {
		if want := []string{"kubectl", "get", "node", "test-node", "-o", "json"}; !reflect.DeepEqual(cmd.Args, want) {
			t.Fatalf("command = %v, want %v", cmd.Args, want)
		}
		return response, nil
	}
	if err := logicalFlowGetWithRunner(run, &node, "node", "test-node"); err != nil {
		t.Fatal(err)
	}
	if !node.Spec.Unschedulable || len(node.Labels) != 1 || len(node.Spec.Taints) != 1 {
		t.Fatal("target snapshot was not decoded")
	}
	response = `{"metadata":{"name":"survivor"},"spec":{}}`
	if err := logicalFlowGetWithRunner(run, &node, "node", "test-node"); err != nil {
		t.Fatal(err)
	}
	if node.Name != "survivor" || node.Spec.Unschedulable || node.Labels != nil || node.Spec.Taints != nil {
		t.Fatalf("omitted fields retained prior values: %+v", node)
	}
	response = `{"metadata":{"labels":{"new":"label"}},"spec":{"taints":[{"key":"new","effect":"NoSchedule"}]}}`
	if err := logicalFlowGetWithRunner(run, &node, "node", "test-node"); err != nil {
		t.Fatal(err)
	}
	response = `{}`
	if err := logicalFlowGetWithRunner(run, &node, "node", "test-node"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(node, corev1.Node{}) {
		t.Fatal("repeated read did not clear omitted maps/slices")
	}
}

func TestLogicalFlowGetFailurePreservesSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name, response string
		err            error
	}{
		{"malformed JSON", `{"metadata":`, nil},
		{"type error after partial decode", `{"metadata":{"name":"changed"},"spec":{"unschedulable":"invalid"}}`, nil},
		{"command failure", `{"metadata":{"name":"changed"}}`, errors.New("kubectl failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var node corev1.Node
			node.Name = "prior"
			node.Labels = map[string]string{"retain": "me"}
			node.Spec.Unschedulable = true
			node.Spec.Taints = []corev1.Taint{{Key: "retain", Effect: corev1.TaintEffectNoSchedule}}
			before := node.DeepCopy()
			run := func(*exec.Cmd) (string, error) { return tc.response, tc.err }
			if err := logicalFlowGetWithRunner(run, &node, "node", "prior"); err == nil {
				t.Fatal("expected failed read")
			}
			if !reflect.DeepEqual(node, *before) {
				t.Fatal("failed read changed the prior snapshot")
			}
		})
	}
}
