//go:build hadron_smoke
// +build hadron_smoke

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

// VM-4's last remaining item after TestHadronOutageFlowTwoNodeDrainsWorkload closed the two-guest
// topology and real drain/eviction: "network-policy ... assertions remain open" -- a deliberately
// separate assertion from drain/eviction, per that milestone's own doc comment, so a live failure
// here is never ambiguous about which of two new mechanisms broke.
//
// This proves the real NUTServer NetworkPolicy (internal/controller/nutserver_networkpolicy.go)
// actually restricts traffic in the live two-guest topology, not merely that the object exists.
// That policy's own ingress rule allows exactly two peers: pods in the NUTServer's own namespace,
// and the operator's manager pod (any namespace, matched by label) -- everything else is denied.
// Testing this cross-node (probe pods scheduled on the agent, NUTServer pinned to the server) is
// the point: VM-4's own text asks for network policy "in the two-guest topology" specifically,
// distinct from a single-guest check that could pass on loopback alone.
//
// Mirrors test/e2e/network_policy_test.go's own reasoning for why a positive and a negative case
// together are what makes either one mean anything: a same-namespace pod that reaches the real
// upsd port, and an unrelated-namespace pod that reliably cannot, at the same time against the
// same real policy. If the CNI did not enforce NetworkPolicy at all, the negative case would
// succeed too; if the fixture were simply broken, the positive case would fail too. Reusing the
// operator's own policy rather than a synthetic one is deliberate here (unlike that e2e spec):
// the goal is proving this specific rendered policy is correct and enforced, not just that the
// cluster can enforce policies in general -- kube-router, k3s's own bundled NetworkPolicy
// controller, already establishes the latter (confirmed against k3s's own documentation, not
// assumed) and applies to every guest this harness boots without extra configuration.
package hadron

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

// networkPolicyOutageNamespace hosts the UPSDevice/NUTServer fixture and the authorized (same-
// namespace) probe pod. networkPolicyUnauthorizedNamespace hosts only the unauthorized probe --
// kept separate so the NetworkPolicy's own same-namespace allow rule cannot accidentally cover it.
const (
	networkPolicyOutageNamespace       = "power-outage-netpol-hadron"
	networkPolicyUnauthorizedNamespace = "hadron-outage-netpol-unauthorized"
)

// TestHadronOutageFlowTwoNodeEnforcesNetworkPolicy boots a real two-node k3s cluster, deploys the
// real manager and a real NUTServer pinned to the server (survivor) node, and confirms from the
// agent node that the real rendered NetworkPolicy permits a same-namespace probe and denies an
// unrelated-namespace one against the same live upsd port.
func TestHadronOutageFlowTwoNodeEnforcesNetworkPolicy(t *testing.T) {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if deadline, ok := t.Deadline(); ok {
		var deadlineCancel context.CancelFunc
		ctx, deadlineCancel = context.WithDeadline(ctx, deadline.Add(-time.Minute))
		defer deadlineCancel()
	}

	repoRoot := repoRootDir(t)
	managerImage, managerTar := buildManagerImageTarball(ctx, t, repoRoot)
	nutServerImage, nutServerTar := buildOperandImageTarball(ctx, t, repoRoot, "images/nut-server/Dockerfile", "nutserver")
	allTars := []string{managerTar, nutServerTar}

	serverCreds, agentCreds, kubeconfigPath, clientset, serverNodeName, agentNodeName := bootAndJoinTwoNodeCluster(ctx, t)

	t.Log("waiting for the default namespace's own default ServiceAccount")
	waitForWithDiagnostics(t, ctx, 2*time.Minute, "default ServiceAccount", func(ctx context.Context) error {
		_, err := clientset.CoreV1().ServiceAccounts("default").Get(ctx, "default", metav1.GetOptions{})
		return err
	}, nil)

	t.Log("importing the real manager and nut-server images into both guests' own containerd")
	for _, tarPath := range allTars {
		importImageTarball(ctx, t, serverCreds, tarPath)
		importImageTarball(ctx, t, agentCreds, tarPath)
	}

	restore := preserveFile(t, filepath.Join(repoRoot, "config", "manager", "kustomization.yaml"))
	defer restore()

	t.Log("make install (CRDs)")
	runMake(ctx, t, repoRoot, kubeconfigPath, nil, "install")

	t.Log("make deploy-byo-cert (RBAC, manager Deployment, webhook cert via hack/webhook-cert.sh)")
	runMake(ctx, t, repoRoot, kubeconfigPath, []string{"IMG=" + managerImage}, "deploy-byo-cert")

	t.Log("pinning the manager Deployment to the survivor (server) node")
	runKubectl(ctx, t, kubeconfigPath, "-n", operatorNamespace, "patch", "deployment", operatorDeployment,
		"--type=strategic", "-p", fmt.Sprintf(`{"spec":{"template":{"spec":{"nodeSelector":{"kubernetes.io/hostname":%q}}}}}`, serverNodeName))

	t.Log("waiting for the real controller-manager Deployment to become Ready")
	waitForWithDiagnostics(t, ctx, 3*time.Minute, "controller-manager Ready", func(ctx context.Context) error {
		dep, err := clientset.AppsV1().Deployments(operatorNamespace).Get(ctx, operatorDeployment, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if dep.Status.ReadyReplicas < 1 {
			return fmt.Errorf("controller-manager not ready yet: readyReplicas=%d", dep.Status.ReadyReplicas)
		}
		return nil
	}, func(ctx context.Context) {
		out := runKubectlOutput(ctx, t, kubeconfigPath, "get", "pods", "-n", operatorNamespace, "-o", "wide")
		t.Logf("diagnostic controller-manager pod listing:\n%s", out)
	})

	t.Log("creating the fixture and unauthorized-probe namespaces")
	runKubectl(ctx, t, kubeconfigPath, "create", "ns", networkPolicyOutageNamespace)
	runKubectl(ctx, t, kubeconfigPath, "create", "ns", networkPolicyUnauthorizedNamespace)

	nutServerRepo, nutServerTag := splitImageRef(t, nutServerImage)
	const serverName = "hadron-netpol-nutserver"

	// A minimal UPSDevice/NUTServer, no NodePowerAgent/ShutdownFlow -- this milestone tests one
	// rendered NetworkPolicy, not the outage/drain/actuation chain the sibling milestone already
	// proved.
	fixture := fmt.Sprintf(`
apiVersion: v1
kind: ConfigMap
metadata:
  name: hadron-netpol-transitions
  namespace: %[1]s
data:
  sequence.seq: |
    device.mfr: nut-operator
    device.model: hadron-netpol
    ups.mfr: nut-operator
    ups.model: hadron-netpol
    ups.status: OL
    battery.charge: 100
    battery.runtime: 3600
    ups.load: 10
---
apiVersion: power.zalud.io/v1alpha1
kind: UPSDevice
metadata:
  name: hadron-netpol-ups
spec:
  displayName: Hadron VM-4 NetworkPolicy Dummy UPS
  driver: dummy-ups
  powerDomains:
    - hadron-domain
  simulation:
    sequenceConfigMapRef:
      namespace: %[1]s
      name: hadron-netpol-transitions
  telemetry:
    pollInterval: 5s
    alertPollInterval: 5s
---
apiVersion: power.zalud.io/v1alpha1
kind: NUTServer
metadata:
  name: %[5]s
spec:
  namespace: %[1]s
  deviceRefs:
    - name: hadron-netpol-ups
  image:
    repository: %[2]s
    tag: "%[3]s"
    pullPolicy: IfNotPresent
  auth:
    mode: OperatorManaged
  tls:
    mode: Disabled
  placement:
    nodeSelector:
      kubernetes.io/hostname: %[4]s
`, networkPolicyOutageNamespace, nutServerRepo, nutServerTag, serverNodeName, serverName)

	t.Log("applying the real UPSDevice/NUTServer fixture")
	waitForWithDiagnostics(t, ctx, 2*time.Minute, "fixture apply", func(ctx context.Context) error {
		attemptCtx, attemptCancel := context.WithTimeout(ctx, 15*time.Second)
		defer attemptCancel()
		cmd := exec.CommandContext(attemptCtx, "kubectl", "apply", "-f", "-")
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfigPath)
		cmd.Stdin = strings.NewReader(fixture)
		out, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("kubectl apply: %w\n%s", err, out)
		}
		return nil
	}, nil)

	t.Log("waiting for the real rendered NetworkPolicy to exist")
	const policyName = serverName + "-nut-server"
	waitForWithDiagnostics(t, ctx, 2*time.Minute, "NUTServer NetworkPolicy rendered", func(ctx context.Context) error {
		_, err := clientset.NetworkingV1().NetworkPolicies(networkPolicyOutageNamespace).Get(ctx, policyName, metav1.GetOptions{})
		return err
	}, nil)

	t.Log("waiting for the real NUTServer Deployment to become Ready")
	waitForWithDiagnostics(t, ctx, 3*time.Minute, "NUTServer Ready", func(ctx context.Context) error {
		dep, err := clientset.AppsV1().Deployments(networkPolicyOutageNamespace).Get(ctx, serverName+"-nut-server", metav1.GetOptions{})
		if err != nil {
			return err
		}
		if dep.Status.ReadyReplicas < 1 {
			return fmt.Errorf("NUTServer not ready yet: readyReplicas=%d", dep.Status.ReadyReplicas)
		}
		return nil
	}, func(ctx context.Context) {
		out := runKubectlOutput(ctx, t, kubeconfigPath, "get", "pods", "-n", networkPolicyOutageNamespace, "-o", "wide")
		t.Logf("diagnostic NUTServer pod listing:\n%s", out)
	})

	t.Log("reading the real NUTServer Service's ClusterIP")
	clusterIP := strings.TrimSpace(runKubectlOutput(ctx, t, kubeconfigPath, "get", "svc", serverName,
		"-n", networkPolicyOutageNamespace, "-o", "jsonpath={.spec.clusterIP}"))
	if clusterIP == "" {
		t.Fatalf("NUTServer Service has no ClusterIP")
	}

	t.Log("starting the authorized (same-namespace) probe pod on the agent node")
	applyProbePod(ctx, t, kubeconfigPath, clientset, networkPolicyOutageNamespace, "netpol-authorized-probe", agentNodeName, nutServerImage)

	t.Log("starting the unauthorized (unrelated-namespace) probe pod on the agent node")
	applyProbePod(ctx, t, kubeconfigPath, clientset, networkPolicyUnauthorizedNamespace, "netpol-unauthorized-probe", agentNodeName, nutServerImage)

	// Returns nc's own combined output alongside the error: "exit status 1" alone does not
	// distinguish a timeout (the -w budget firing, consistent with a real deny) from a DNS/exec
	// failure (consistent with the probe pod or command itself being broken), and the first live
	// run of this test had only the bare error to go on.
	probe := func(namespace, podName string) (string, error) {
		attemptCtx, attemptCancel := context.WithTimeout(ctx, 10*time.Second)
		defer attemptCancel()
		cmd := exec.CommandContext(attemptCtx, "kubectl", "-n", namespace, "exec", podName, "--",
			"nc", "-z", "-v", "-w", "3", clusterIP, fmt.Sprintf("%d", 3493))
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfigPath)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	t.Log("confirming the same-namespace probe reaches the real upsd port -- otherwise a later denial would prove nothing")
	waitForWithDiagnostics(t, ctx, 2*time.Minute, "authorized probe reaches NUTServer", func(ctx context.Context) error {
		if out, err := probe(networkPolicyOutageNamespace, "netpol-authorized-probe"); err != nil {
			return fmt.Errorf("same-namespace probe could not reach the real upsd port yet: %w\n%s", err, out)
		}
		return nil
	}, func(ctx context.Context) {
		policy := runKubectlOutput(ctx, t, kubeconfigPath, "get", "networkpolicy", policyName,
			"-n", networkPolicyOutageNamespace, "-o", "yaml")
		t.Logf("diagnostic rendered NetworkPolicy:\n%s", policy)
		pods := runKubectlOutput(ctx, t, kubeconfigPath, "get", "pods", "-n", networkPolicyOutageNamespace, "-o", "wide")
		t.Logf("diagnostic pod listing in %s:\n%s", networkPolicyOutageNamespace, pods)
		// Both probes run cross-node (probe pod on the agent, NUTServer on the server); if both
		// nodes' InternalIP is identical, they registered the isolated per-guest NAT address
		// instead of a distinct ClusterLink address, and cross-node pod traffic (Flannel's own
		// VXLAN encapsulation target) would be unrouteable regardless of any NetworkPolicy.
		nodes := runKubectlOutput(ctx, t, kubeconfigPath, "get", "nodes", "-o", "wide")
		t.Logf("diagnostic node listing (watch for identical InternalIP values):\n%s", nodes)
		dumpKubeProxyState(ctx, t, agentCreds, clusterIP)
	})

	t.Log("confirming the unrelated-namespace probe is reliably denied, not merely slow")
	const denialChecks = 6
	for i := 0; i < denialChecks; i++ {
		if _, err := probe(networkPolicyUnauthorizedNamespace, "netpol-unauthorized-probe"); err == nil {
			t.Fatalf("an unrelated-namespace pod reached the real upsd port on attempt %d/%d -- the "+
				"rendered NetworkPolicy did not deny it (or this guest's CNI does not enforce "+
				"NetworkPolicy at all, which the same-namespace pass above rules out as the whole "+
				"cluster being broken)", i+1, denialChecks)
		}
		time.Sleep(5 * time.Second)
	}
	t.Log("confirmed: the real rendered NetworkPolicy allowed the same-namespace probe and denied the unrelated-namespace one")
}

// applyProbePod starts a minimal pod on nodeName carrying only what the nc-based probe above
// needs. image is the already-imported nut-server operand image (Alpine-based, ships busybox nc)
// so this adds no extra pull or registry dependency.
func applyProbePod(ctx context.Context, t *testing.T, kubeconfigPath string, clientset *kubernetes.Clientset, namespace, podName, nodeName, image string) {
	t.Helper()
	manifest := fmt.Sprintf(`
apiVersion: v1
kind: Pod
metadata:
  name: %[1]s
  namespace: %[2]s
spec:
  nodeSelector:
    kubernetes.io/hostname: %[3]s
  terminationGracePeriodSeconds: 1
  containers:
    - name: probe
      image: %[4]s
      imagePullPolicy: IfNotPresent
      command: ["sleep", "1800"]
`, podName, namespace, nodeName, image)
	cmd := exec.CommandContext(ctx, "kubectl", "apply", "-f", "-")
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfigPath)
	cmd.Stdin = strings.NewReader(manifest)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("kubectl apply probe pod %s: %v\n%s", podName, err, out)
	}

	// A typed Get, not a kubectl/jsonpath shellout: outage_flow_two_node_smoke_test.go's own
	// TestHadronOutageFlowTwoNodeDrainsWorkload found live (2026-09-18) that a kubectl jsonpath
	// query against a not-yet-existent object errors out, and runKubectlOutput turns that into an
	// immediate t.Fatalf instead of a retryable error -- aborting the whole test on a normal,
	// momentary race between kubectl apply returning and the object actually existing.
	waitForWithDiagnostics(t, ctx, 2*time.Minute, podName+" Running", func(ctx context.Context) error {
		pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
		if err != nil {
			return err
		}
		if pod.Status.Phase != corev1.PodRunning {
			return fmt.Errorf("probe pod phase=%q, not Running yet", pod.Status.Phase)
		}
		return nil
	}, nil)
}
