//go:build e2e

package e2e

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	_ "embed"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"os/exec"
	"strings"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/test/utils"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
)

//go:embed fixtures/nut_client.py
var nutOnlyClientSource string

const nutOnlySpecName = "serves managed NUT telemetry with only the NUT-only profile APIs and permissions"
const nutOnlyNS = "mod4-operands"
const nutOnlyClientNS = "mod4-clients"
const nutOnlyDeniedNS = "mod4-denied"

var _ = Describe("NUT-only", Ordered, Serial, func() {
	It(nutOnlySpecName, Label("MOD-4"), func() {
		By("generating and installing only the NUT-only profile")
		_, err := utils.Run(exec.Command("make", "build-installer-nut-only", "IMG="+managerImage, "NUT_SERVER_IMG="+nutServerImage))
		Expect(err).NotTo(HaveOccurred())
		DeferCleanup(func() {
			for _, args := range [][]string{
				{"delete", "nutservers", "mod4", "mod4-relay", "mod4-late", "--ignore-not-found=true", "--wait=true", "--timeout=2m"},
				{"delete", "upsdevices", "mod4-ups", "mod4-extra", "mod4-relayed", "mod4-late", "--ignore-not-found=true", "--wait=true", "--timeout=2m"},
				{"delete", "namespace", nutOnlyNS, nutOnlyClientNS, nutOnlyDeniedNS, "--ignore-not-found=true", "--wait=true", "--timeout=2m"},
				{"delete", "-f", "dist/install-nut-only.yaml", "--ignore-not-found=true", "--wait=true", "--timeout=2m"},
			} {
				_, cleanupErr := utils.Run(exec.Command("kubectl", append([]string{"--request-timeout=15s"}, args...)...))
				Expect(cleanupErr).NotTo(HaveOccurred())
			}
		})
		defer func() {
			if CurrentSpecReport().Failed() {
				utils.DumpNamespaceDiagnostics(nutOnlyNS)
				utils.DumpNamespaceDiagnostics(namespace)
			}
		}()
		_, err = utils.Run(exec.Command("kubectl", "apply", "-f", "dist/install-nut-only.yaml"))
		Expect(err).NotTo(HaveOccurred())
		_, err = utils.Run(exec.Command("bash", "hack/webhook-cert.sh"))
		Expect(err).NotTo(HaveOccurred())
		_, err = utils.Run(exec.Command("kubectl", "rollout", "status", "deployment/nut-operator-controller-manager", "-n", namespace, "--timeout=3m"))
		Expect(err).NotTo(HaveOccurred())
		nutOnlyAssertBoundary()
		for _, ns := range []string{nutOnlyNS, nutOnlyClientNS, nutOnlyDeniedNS} {
			Expect(applyFixtureManifest(logicalFlowJSONList(corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: ns}}))).To(Succeed())
		}
		ca, key := nutOnlyCA()
		certificate, fingerprint := nutOnlyCertificate(ca, key)
		Expect(applyFixtureManifest(logicalFlowJSONList(certificate))).To(Succeed())
		device := nutOnlyDevice("mod4-ups")
		server := nutOnlyServer("mod4", device.Name)
		Expect(applyFixtureManifest(logicalFlowJSONList(device, server))).To(Succeed())
		pod := nutOnlyReady("mod4")
		Expect(pod.Spec.Containers).To(HaveLen(2))
		for _, container := range pod.Spec.Containers {
			Expect(container.Image).To(Equal(nutServerImage))
		}
		var users corev1.Secret
		Expect(logicalFlowGet(&users, "secret", "mod4-nut-users", "-n", nutOnlyNS)).To(Succeed())
		Expect(users.Data).To(HaveKey("admin-password"))
		password := string(users.Data["monitor-password"])
		for _, ns := range []string{nutOnlyClientNS, nutOnlyDeniedNS} {
			Expect(applyFixtureManifest(nutOnlyClientManifest(ns, ca, password))).To(Succeed())
			logicalFlowReadyPod(ns, "app=mod4-client")
		}
		By("reading real telemetry with verified TLS/auth and enforced client policy")
		nutOnlyReadEventually("mod4", "mod4-ups", "ups.status", "OL", fingerprint)
		for _, mode := range []string{"wrong-ca", "wrong-name", "bad-password"} {
			out, err := nutOnlyQuery(nutOnlyClientNS, "mod4", "mod4-ups", "ups.status", mode)
			Expect(err).To(HaveOccurred())
			if mode != "bad-password" {
				Expect(out).To(ContainSubstring("SSLCertVerificationError"))
			} else {
				Expect(out).To(ContainSubstring("authentication refused"))
			}
		}
		out, err := nutOnlyQuery(nutOnlyDeniedNS, "mod4", "mod4-ups", "ups.status", "normal")
		Expect(err).To(HaveOccurred())
		Expect(out).To(ContainSubstring("TimeoutError"))

		By("updating and adding/removing devices without a UPSDevice polling controller")
		device.Spec.DisplayName = "updated-model"
		Expect(applyFixtureManifest(logicalFlowJSONList(device))).To(Succeed())
		nutOnlyReadEventually("mod4", "mod4-ups", "ups.model", "updated-model", fingerprint)
		extra := nutOnlyDevice("mod4-extra")
		server.Spec.DeviceRefs = append(server.Spec.DeviceRefs, power.ObjectNameReference{Name: extra.Name})
		Expect(applyFixtureManifest(logicalFlowJSONList(extra, server))).To(Succeed())
		nutOnlyReadEventually("mod4", "mod4-extra", "ups.status", "OL", fingerprint)
		server.Spec.DeviceRefs = server.Spec.DeviceRefs[:1]
		Expect(applyFixtureManifest(logicalFlowJSONList(server))).To(Succeed())
		Eventually(func(g Gomega) {
			out, err := nutOnlyQuery(nutOnlyClientNS, "mod4", "mod4-extra", "ups.status", "normal")
			g.Expect(err).To(HaveOccurred())
			g.Expect(out).To(ContainSubstring("UNKNOWN-UPS"))
		}, 3*time.Minute, 2*time.Second).Should(Succeed())
		var observed power.UPSDevice
		Expect(logicalFlowGet(&observed, "upsdevice", device.Name)).To(Succeed())
		Expect(observed.Status.Phase).To(BeEmpty())
		Expect(recoveryPodUnchanged(pod, nutOnlyReady("mod4"))).To(Succeed())

		By("recovering an isolated driver without restarting upsd or its pod")
		out, err = utils.Run(exec.Command("kubectl", "exec", "-n", nutOnlyNS, pod.Name, "-c", "driver-supervisor", "--", "sh", "-c", recoveryDriverSampleScript))
		Expect(err).NotTo(HaveOccurred())
		before, err := parseRecoveryDriver(out)
		Expect(err).NotTo(HaveOccurred())
		_, err = utils.Run(exec.Command("kubectl", "exec", "-n", nutOnlyNS, pod.Name, "-c", "driver-supervisor", "--", "sh", "-c", recoveryDriverKillScript, "--", before.pid, before.startTicks))
		Expect(err).NotTo(HaveOccurred())
		Eventually(func(g Gomega) {
			out, err := utils.Run(exec.Command("kubectl", "exec", "-n", nutOnlyNS, pod.Name, "-c", "driver-supervisor", "--", "sh", "-c", recoveryDriverSampleScript))
			g.Expect(err).NotTo(HaveOccurred())
			after, err := parseRecoveryDriver(out)
			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(after.pid + ":" + after.startTicks).NotTo(Equal(before.pid + ":" + before.startTicks))
		}, driverRecoveryBudget, time.Second).Should(Succeed())
		Expect(recoveryPodUnchanged(pod, nutOnlyReady("mod4"))).To(Succeed())

		By("rotating ExistingSecret credentials and the serving certificate")
		newPassword := "synthetic-mod4-rotated"
		Expect(applyFixtureManifest(nutOnlyUsers(password))).To(Succeed())
		server.Spec.Auth = power.NUTAuthSpec{Mode: power.NUTAuthExistingSecret, ExistingSecretRef: &power.NamespacedNameReference{Namespace: nutOnlyNS, Name: "mod4-existing-users"}}
		Expect(applyFixtureManifest(logicalFlowJSONList(server))).To(Succeed())
		nutOnlyReady("mod4")
		nutOnlyReadEventually("mod4", "mod4-ups", "ups.status", "OL", fingerprint)
		Expect(applyFixtureManifest(nutOnlyUsers(newPassword))).To(Succeed())
		Expect(applyFixtureManifest(logicalFlowJSONList(corev1.Secret{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}, ObjectMeta: metav1.ObjectMeta{Name: "mod4-credentials", Namespace: nutOnlyClientNS}, StringData: map[string]string{"password": newPassword, "old-password": password}}))).To(Succeed())
		nutOnlyReadEventually("mod4", "mod4-ups", "ups.status", "OL", fingerprint)
		Eventually(func(g Gomega) {
			out, err := nutOnlyQuery(nutOnlyClientNS, "mod4", "mod4-ups", "ups.status", "old-password")
			g.Expect(err).To(HaveOccurred())
			g.Expect(out).To(ContainSubstring("authentication refused"))
		}, 3*time.Minute, 2*time.Second).Should(Succeed())
		certificate, fingerprint = nutOnlyCertificate(ca, key)
		Expect(applyFixtureManifest(logicalFlowJSONList(certificate))).To(Succeed())
		nutOnlyReadEventually("mod4", "mod4-ups", "ups.status", "OL", fingerprint)

		By("qualifying the generated upstream relay config and unsupported auth rejection")
		nutOnlyRelayAcceptance(server, fingerprint)
		By("reapplying the profile and restarting its manager without changing operand state")
		beforeUpgrade := nutOnlyReady("mod4")
		_, err = utils.Run(exec.Command("kubectl", "apply", "-f", "dist/install-nut-only.yaml"))
		Expect(err).NotTo(HaveOccurred())
		_, err = utils.Run(exec.Command("bash", "hack/webhook-cert.sh"))
		Expect(err).NotTo(HaveOccurred())
		_, err = utils.Run(exec.Command("kubectl", "rollout", "restart", "deployment/nut-operator-controller-manager", "-n", namespace))
		Expect(err).NotTo(HaveOccurred())
		_, err = utils.Run(exec.Command("kubectl", "rollout", "status", "deployment/nut-operator-controller-manager", "-n", namespace, "--timeout=3m"))
		Expect(err).NotTo(HaveOccurred())
		nutOnlyReadEventually("mod4", "mod4-ups", "ups.status", "OL", fingerprint)
		Expect(recoveryPodUnchanged(beforeUpgrade, nutOnlyReady("mod4"))).To(Succeed())
		nutOnlyAssertBoundary()
	})
})

func nutOnlyDevice(name string) power.UPSDevice {
	return power.UPSDevice{TypeMeta: metav1.TypeMeta{APIVersion: "power.zalud.io/v1alpha1", Kind: "UPSDevice"}, ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: power.UPSDeviceSpec{Driver: "dummy-ups", DisplayName: "mod4-fixture"}}
}

func nutOnlyServer(name, device string) power.NUTServer {
	return power.NUTServer{TypeMeta: metav1.TypeMeta{APIVersion: "power.zalud.io/v1alpha1", Kind: "NUTServer"}, ObjectMeta: metav1.ObjectMeta{Name: name}, Spec: power.NUTServerSpec{Namespace: nutOnlyNS, DeviceRefs: []power.ObjectNameReference{{Name: device}},
		TLS:          power.NUTTLSSpec{Mode: power.NUTTLSRequired, ServerCertificateRef: &power.NamespacedNameReference{Namespace: nutOnlyNS, Name: "mod4-tls"}},
		ClientAccess: []power.NUTClientPeer{{NamespaceSelector: metav1.LabelSelector{MatchLabels: map[string]string{"kubernetes.io/metadata.name": nutOnlyClientNS}}, PodSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "mod4-client"}}}}}}
}

func nutOnlyReady(name string) corev1.Pod {
	Eventually(func(g Gomega) {
		var s power.NUTServer
		g.Expect(logicalFlowGet(&s, "nutserver", name)).To(Succeed())
		g.Expect(s.Status.ObservedGeneration).To(Equal(s.Generation))
		g.Expect(s.Status.Phase).To(Equal(power.NUTServerPhaseReady))
	}, 3*time.Minute, 2*time.Second).Should(Succeed())
	return logicalFlowReadyPod(nutOnlyNS, "power.zalud.io/nutserver="+name)
}

func nutOnlyQuery(ns, server, ups, variable, mode string) (string, error) {
	return utils.Run(exec.Command("kubectl", "exec", "-n", ns, "mod4-client", "--", "python3", "-B", "/fixture/client.py", server+"."+nutOnlyNS+".svc", ups, variable, mode))
}

func nutOnlyReadEventually(server, ups, variable, value, fingerprint string) {
	Eventually(func(g Gomega) {
		out, err := nutOnlyQuery(nutOnlyClientNS, server, ups, variable, "normal")
		g.Expect(err).NotTo(HaveOccurred())
		var result map[string]string
		g.Expect(json.Unmarshal([]byte(out), &result)).To(Succeed())
		g.Expect(result["value"]).To(Equal(value))
		g.Expect(result["certificate"]).To(Equal(fingerprint))
	}, 3*time.Minute, 2*time.Second).Should(Succeed())
}

func nutOnlyUsers(password string) string {
	return logicalFlowJSONList(corev1.Secret{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}, ObjectMeta: metav1.ObjectMeta{Name: "mod4-existing-users", Namespace: nutOnlyNS}, StringData: map[string]string{"upsd.users": "[monitor]\n password = " + password + "\n upsmon secondary\n"}})
}

func nutOnlyAssertBoundary() {
	out, err := utils.Run(exec.Command("kubectl", "get", "crds", "-o", "jsonpath={range .items[*]}{.metadata.name}{\"\\n\"}{end}"))
	Expect(err).NotTo(HaveOccurred())
	var powerCRDs []string
	for _, name := range strings.Fields(out) {
		if strings.HasSuffix(name, ".power.zalud.io") {
			powerCRDs = append(powerCRDs, name)
		}
	}
	Expect(powerCRDs).To(ConsistOf("nutservers.power.zalud.io", "upsdevices.power.zalud.io"))
	for _, resource := range []string{"nodes", "nodepoweragents.power.zalud.io", "shutdownflows.power.zalud.io", "powermanagementclusters.power.zalud.io"} {
		out, _ := utils.Run(exec.Command("kubectl", "auth", "can-i", "patch", resource, "--as=system:serviceaccount:"+namespace+":"+serviceAccountName))
		lines := strings.Split(strings.TrimSpace(out), "\n")
		Expect(lines[len(lines)-1]).To(Equal("no"))
	}
	Eventually(func(g Gomega) {
		out, err = utils.Run(exec.Command("kubectl", "logs", "deployment/nut-operator-controller-manager", "-n", namespace))
		g.Expect(err).NotTo(HaveOccurred())
		g.Expect(out).To(ContainSubstring(`"controller": "nutserver"`))
	}, time.Minute, time.Second).Should(Succeed())
	for _, omitted := range []string{"upsdevice", "shutdownflow", "nodepoweragent", "powermanagementcluster", "nodehalt", "powerinventorynode", "upscapabilityprofile"} {
		Expect(out).NotTo(ContainSubstring(`"controller": "` + omitted + `"`))
	}
	Expect(out).NotTo(ContainSubstring("Failed to watch"))
	var deployment appsv1.Deployment
	Expect(logicalFlowGet(&deployment, "deployment", "nut-operator-controller-manager", "-n", namespace)).To(Succeed())
	Expect(deployment.Spec.Template.Spec.Containers[0].Args).To(ContainElement("--profile=nut-only"))
}

func nutOnlyCA() ([]byte, *ecdsa.PrivateKey) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	Expect(err).NotTo(HaveOccurred())
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "mod4-fixture-ca"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	Expect(err).NotTo(HaveOccurred())
	return der, key
}

func nutOnlyCertificate(caDER []byte, caKey *ecdsa.PrivateKey) (corev1.Secret, string) {
	ca, err := x509.ParseCertificate(caDER)
	Expect(err).NotTo(HaveOccurred())
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	Expect(err).NotTo(HaveOccurred())
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	Expect(err).NotTo(HaveOccurred())
	template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "mod4"}, DNSNames: []string{"mod4." + nutOnlyNS + ".svc", "mod4-relay." + nutOnlyNS + ".svc", "mod4-late." + nutOnlyNS + ".svc"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, &key.PublicKey, caKey)
	Expect(err).NotTo(HaveOccurred())
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	Expect(err).NotTo(HaveOccurred())
	return corev1.Secret{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}, ObjectMeta: metav1.ObjectMeta{Name: "mod4-tls", Namespace: nutOnlyNS}, Type: corev1.SecretTypeTLS, Data: map[string][]byte{"tls.crt": pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), "tls.key": pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})}}, fmt.Sprintf("%x", sha256.Sum256(der))
}

func nutOnlyClientManifest(ns string, ca []byte, password string) string {
	meta := metav1.ObjectMeta{Name: "mod4-client", Namespace: ns}
	pod := corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: meta, Spec: corev1.PodSpec{AutomountServiceAccountToken: ptr.To(false), SecurityContext: &corev1.PodSecurityContext{RunAsUser: ptr.To(int64(65532)), RunAsNonRoot: ptr.To(true), SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault}}, Containers: []corev1.Container{{Name: "client", Image: snmpsimFixtureImage, ImagePullPolicy: corev1.PullIfNotPresent, Command: []string{"sleep", "3600"}, SecurityContext: &corev1.SecurityContext{ReadOnlyRootFilesystem: ptr.To(true), AllowPrivilegeEscalation: ptr.To(false), Capabilities: &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}}}, VolumeMounts: []corev1.VolumeMount{{Name: "fixture", MountPath: "/fixture", ReadOnly: true}, {Name: "credentials", MountPath: "/credentials", ReadOnly: true}}}}, Volumes: []corev1.Volume{{Name: "fixture", VolumeSource: corev1.VolumeSource{ConfigMap: &corev1.ConfigMapVolumeSource{LocalObjectReference: corev1.LocalObjectReference{Name: "mod4-client"}}}}, {Name: "credentials", VolumeSource: corev1.VolumeSource{Secret: &corev1.SecretVolumeSource{SecretName: "mod4-credentials"}}}}}}
	pod.Labels = map[string]string{"app": "mod4-client"}
	return logicalFlowJSONList(pod, corev1.ConfigMap{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "ConfigMap"}, ObjectMeta: meta, Data: map[string]string{"client.py": nutOnlyClientSource, "ca.crt": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca}))}}, corev1.Secret{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Secret"}, ObjectMeta: metav1.ObjectMeta{Name: "mod4-credentials", Namespace: ns}, StringData: map[string]string{"password": password, "old-password": password}})
}
