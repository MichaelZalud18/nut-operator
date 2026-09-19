//go:build e2e

package e2e

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/test/utils"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"
)

//go:embed fixtures/mod2_receiver.py
var modularReceiverSource string

const modularReceiverName = "mod2-receiver"

var modularHookNames = []string{"external-stop", "external-repeat", "external-fail", "external-timeout", "external-disallowed"}

func modularReceiverManifest(survivor string) string {
	labels := map[string]string{"app": modularReceiverName}
	meta := metav1.ObjectMeta{Name: modularReceiverName, Namespace: flowOperandNamespace}
	pod := corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: meta,
		Spec: corev1.PodSpec{NodeSelector: map[string]string{"kubernetes.io/hostname": survivor}, AutomountServiceAccountToken: ptr.To(false),
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(int64(65532)), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			Containers: []corev1.Container{{Name: "receiver", Image: snmpsimFixtureImage, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"python3", "-B", "/fixture/receiver.py"},
				SecurityContext: &corev1.SecurityContext{AllowPrivilegeEscalation: ptr.To(false), ReadOnlyRootFilesystem: ptr.To(true), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				Env:             []corev1.EnvVar{{Name: "HOOK_TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: modularReceiverName}, Key: "token"}}}},
				VolumeMounts:    []corev1.VolumeMount{{Name: "fixture", MountPath: "/fixture", ReadOnly: true}},
				ReadinessProbe:  &corev1.Probe{ProbeHandler: corev1.ProbeHandler{HTTPGet: &corev1.HTTPGetAction{Path: "/state", Port: intstr.FromInt32(8080)}}},
			}}, Volumes: []corev1.Volume{{Name: "fixture", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: modularReceiverName}}}}}}}
	pod.Labels = labels
	service := corev1.Service{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Service"}, ObjectMeta: meta, Spec: corev1.ServiceSpec{Selector: labels, Ports: []corev1.ServicePort{{Port: 8080, TargetPort: intstr.FromInt32(8080)}}}}
	policy := networkingv1.NetworkPolicy{TypeMeta: metav1.TypeMeta{APIVersion: "networking.k8s.io/v1", Kind: "NetworkPolicy"}, ObjectMeta: meta,
		Spec: networkingv1.NetworkPolicySpec{PodSelector: metav1.LabelSelector{MatchLabels: labels}, PolicyTypes: []networkingv1.PolicyType{networkingv1.PolicyTypeIngress}, Ingress: []networkingv1.NetworkPolicyIngressRule{{From: []networkingv1.NetworkPolicyPeer{{NamespaceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": namespace}}, PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"control-plane": "controller-manager"}}}}, Ports: []networkingv1.NetworkPolicyPort{{Port: ptr.To(intstr.FromInt32(8080))}}}}}}
	objects := []any{pod, service, policy,
		corev1.ConfigMap{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: meta, Data: map[string]string{"receiver.py": modularReceiverSource}},
		corev1.Secret{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}, ObjectMeta: meta, StringData: map[string]string{"token": "synthetic-mod2", "authorization": "Bearer synthetic-mod2"}},
	}
	for i, name := range modularHookNames {
		path := []string{"/hooks/stop", "/hooks/stop", "/hooks/fail", "/hooks/timeout", "/blocked"}[i]
		hook := power.ShutdownHook{TypeMeta: metav1.TypeMeta{APIVersion: "power.zalud.io/v1alpha1", Kind: "ShutdownHook"}, ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: flowOperandNamespace}, Spec: power.ShutdownHookSpec{
			Invocation: power.ShutdownHookInvocationSpec{Transport: power.ShutdownHookTransportHTTP, Timeout: &metav1.Duration{Duration: time.Second}, HTTP: &power.ShutdownHookHTTPSpec{URL: modularReceiverURL() + path, Data: map[string]string{"host": "external-a", "operation": "ensure-stopped"}, SecretHeaders: []power.ShutdownHookHTTPSecretHeader{{Name: "Authorization", ValueFrom: power.SecretKeyReference{Namespace: flowOperandNamespace, Name: modularReceiverName, Key: "authorization"}}}}},
		}}
		if i == 0 {
			hook.Spec.DryRun = hook.Spec.Invocation.DeepCopy()
			hook.Spec.DryRun.HTTP.URL = modularReceiverURL() + "/hooks/rehearse"
		}
		objects = append(objects, hook)
	}
	return logicalFlowJSONList(objects...)
}

func modularReceiverURL() string {
	return "http://" + modularReceiverName + "." + flowOperandNamespace + ".svc:8080"
}

func logicalMixedFlowManifest(node, mode string, approved bool) string {
	var flow power.ShutdownFlow
	if err := yaml.Unmarshal([]byte(logicalFlowManifest(node, mode, approved)), &flow); err != nil {
		panic(err)
	}
	var groups []power.ShutdownGroup
	for i, name := range modularHookNames {
		next := "scale"
		if i+1 < len(modularHookNames) {
			next = modularHookNames[i+1]
		}
		groups = append(groups, power.ShutdownGroup{Name: name, Action: power.ShutdownStepRunHook, HookRef: &power.NamespacedNameReference{Namespace: flowOperandNamespace, Name: name}, ShutdownTier: ptr.To(int32(3)), Before: []string{next}, Timeout: &metav1.Duration{Duration: 2 * time.Second}})
	}
	flow.Spec.Groups = append(groups, flow.Spec.Groups...)
	data, err := json.Marshal(flow)
	if err != nil {
		panic(err)
	}
	return string(data)
}

type modularReceiverState struct {
	Effects  int `json:"effects"`
	Requests []struct {
		Execution string `json:"execution"`
		Group     string `json:"group"`
		DryRun    bool   `json:"dryRun"`
		Path      string `json:"path"`
	} `json:"requests"`
}

func modularReceiverRead(g Gomega) modularReceiverState {
	out, err := utils.Run(exec.Command("kubectl", "exec", "-n", flowOperandNamespace, modularReceiverName, "--", "python3", "-c", `import urllib.request; print(urllib.request.urlopen("http://127.0.0.1:8080/state").read().decode())`))
	g.Expect(err).NotTo(HaveOccurred())
	var state modularReceiverState
	g.Expect(json.Unmarshal([]byte(out), &state)).To(Succeed())
	return state
}

func modularHookAudit(execution string) {
	Expect(execution).To(MatchRegexp(`^[a-f0-9-]+$`))
	query := fmt.Sprintf(`SELECT json_agg(row_to_json(a) ORDER BY a.started_at) FROM (SELECT group_name, outcome, started_at, completed_at FROM power.shutdownflow_action_attempts WHERE execution_id='%s' AND (action='RunHook' OR group_name='scale')) a`, execution)
	Eventually(func(g Gomega) {
		out, err := utils.Run(exec.Command("kubectl", "exec", "-n", flowOperandNamespace, "test2-postgres", "--", "psql", "-U", "test2", "-d", "test2", "-tAc", query))
		g.Expect(err).NotTo(HaveOccurred())
		var rows []logicalFlowAttempt
		g.Expect(json.Unmarshal([]byte(out), &rows)).To(Succeed())
		g.Expect(rows).To(HaveLen(6))
		for i, name := range append(append([]string{}, modularHookNames...), "scale") {
			g.Expect(rows[i].Group).To(Equal(name))
			if i < 2 || i == 5 {
				g.Expect(rows[i].Outcome).To(Equal("Succeeded"))
			} else {
				g.Expect(rows[i].Outcome).NotTo(Equal("Succeeded"))
			}
			g.Expect(rows[i].Started.IsZero()).To(BeFalse())
			g.Expect(rows[i].Completed.Before(rows[i].Started)).To(BeFalse())
			if i > 0 {
				g.Expect(rows[i].Started.Before(rows[i-1].Completed)).To(BeFalse())
			}
		}
	}, time.Minute, time.Second).Should(Succeed())
}

func modularAssertReceipts(execution string, dryRun bool) {
	state := modularReceiverRead(Default)
	var groups []string
	for _, request := range state.Requests {
		if request.Execution == execution {
			Expect(request.DryRun).To(Equal(dryRun))
			groups = append(groups, request.Group)
			Expect(strings.HasPrefix(request.Path, "/hooks/")).To(BeTrue())
		}
	}
	if dryRun {
		Expect(groups).To(Equal([]string{"external-stop"}))
		Expect(state.Effects).To(BeZero())
	} else {
		Expect(groups).To(Equal(modularHookNames[:4]))
		Expect(state.Effects).To(Equal(1))
	}
}
