//go:build e2e

package e2e

import (
	"fmt"
	"strings"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/utils/ptr"
)

const startupSpecName = "NS-6: observes rendered NUT startup and monitor reconnects for eleven minutes"
const startupClients = 8

func startupResources(name string) string {
	decoder := yaml.NewYAMLOrJSONDecoder(strings.NewReader(dummyUPSManifest(name, name, name, "NS6 startup fixture")), 4096)
	var device power.UPSDevice
	var server power.NUTServer
	if err := decoder.Decode(&device); err != nil {
		panic(err)
	}
	if err := decoder.Decode(&server); err != nil {
		panic(err)
	}
	server.Spec.Auth = power.NUTAuthSpec{Mode: power.NUTAuthExistingSecret, ExistingSecretRef: &power.NamespacedNameReference{Name: "startup-users", Namespace: name}}
	return logicalFlowJSONList(device, server)
}

func startupClientsManifest(name string) string {
	users := ""
	items := []any{}
	for i := 0; i < startupClients; i++ {
		users += fmt.Sprintf("[observer%d]\n password = startup-fixture-only\n upsmon secondary\n", i)
		config := fmt.Sprintf("MONITOR %s@%s 1 observer%d startup-fixture-only secondary\n", name, name, i) +
			"MINSUPPLIES 1\nPOLLFREQ 1\nPOLLFREQALERT 1\nDEADTIME 15\nSHUTDOWNCMD /bin/false\nPOWERDOWNFLAG /run/nut/powerdown\n"
		items = append(items, corev1.Secret{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
			ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("observer-%d", i), Namespace: name}, StringData: map[string]string{"upsmon.conf": config}}, startupClient(name, i))
	}
	items = append(items, corev1.Secret{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"},
		ObjectMeta: metav1.ObjectMeta{Name: "startup-users", Namespace: name}, StringData: map[string]string{"upsd.users": users}})
	return logicalFlowJSONList(items...)
}

func startupClient(namespace string, index int) corev1.Pod {
	name := fmt.Sprintf("observer-%d", index)
	return corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"},
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace, Labels: map[string]string{"ns6-client": "true"}},
		Spec: corev1.PodSpec{RestartPolicy: corev1.RestartPolicyNever, AutomountServiceAccountToken: ptr.To(false),
			SecurityContext: &corev1.PodSecurityContext{RunAsNonRoot: ptr.To(true), RunAsUser: ptr.To(int64(65532)), RunAsGroup: ptr.To(int64(65532)), FSGroup: ptr.To(int64(65532)), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}},
			Containers: []corev1.Container{{Name: "upsmon", Image: upsmonAgentImage, ImagePullPolicy: corev1.PullIfNotPresent,
				Command:         []string{"upsmon", "-F", "-p"},
				SecurityContext: &corev1.SecurityContext{ReadOnlyRootFilesystem: ptr.To(true), AllowPrivilegeEscalation: ptr.To(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}},
				VolumeMounts:    []corev1.VolumeMount{{Name: "config", MountPath: "/etc/nut", ReadOnly: true}, {Name: "run", MountPath: "/run/nut"}}}},
			Volumes: []corev1.Volume{{Name: "config", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: name}}}, {Name: "run", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}}},
		}}
}
