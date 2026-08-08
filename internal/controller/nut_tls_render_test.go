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

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

func tlsEnabledNUTServer() *powerv1alpha1.NUTServer {
	return &powerv1alpha1.NUTServer{
		ObjectMeta: metav1.ObjectMeta{Name: "rack-a"},
		Spec: powerv1alpha1.NUTServerSpec{
			Namespace: "power-system",
			TLS: powerv1alpha1.NUTTLSSpec{
				Mode: powerv1alpha1.NUTTLSRequired,
				ServerCertificateRef: &powerv1alpha1.NamespacedNameReference{
					Namespace: "power-system",
					Name:      "rack-a-nut-server-tls",
				},
				ServerCARef: &powerv1alpha1.NamespacedNameReference{
					Namespace: "power-system",
					Name:      "rack-a-nut-server-ca",
				},
			},
		},
	}
}

// The bug this guards: spec.tls used to mount a certificate and stop there. upsd only offers
// STARTTLS when CERTFILE names a key pair, so LISTEN-only upsd.conf served plaintext NUT --
// including the MONITOR password every upsmon sends on connect -- while the API reported
// mode: Required.
func TestRenderUPSDConfEmitsCertFileWhenTLSEnabled(t *testing.T) {
	conf := renderUPSDConf(tlsEnabledNUTServer())

	for _, want := range []string{
		"LISTEN 0.0.0.0 3493\n",
		"CERTFILE " + nutServerCombinedCertPath + "\n",
		"DISABLE_WEAK_SSL true\n",
	} {
		if !strings.Contains(conf, want) {
			t.Fatalf("upsd.conf missing %q, got:\n%s", want, conf)
		}
	}
	if strings.Contains(conf, "CERTREQUEST") {
		t.Fatalf("upsd.conf must not request client certificates by default, got:\n%s", conf)
	}
}

func TestRenderUPSDConfEmitsCertRequestOnlyWhenClientVerificationEnabled(t *testing.T) {
	server := tlsEnabledNUTServer()
	server.Spec.TLS.VerifyClientCertificates = ptrBool(true)
	server.Spec.TLS.ClientCARef = &powerv1alpha1.NamespacedNameReference{
		Namespace: "power-system",
		Name:      "rack-a-nut-client-ca",
	}

	conf := renderUPSDConf(server)
	for _, want := range []string{
		"CERTPATH " + nutServerClientCAPath + "\n",
		"CERTREQUEST 2\n",
	} {
		if !strings.Contains(conf, want) {
			t.Fatalf("upsd.conf missing %q, got:\n%s", want, conf)
		}
	}
}

func TestRenderUPSDConfOmitsWeakProtocolDirectiveWhenOptedOut(t *testing.T) {
	server := tlsEnabledNUTServer()
	server.Spec.TLS.DisableWeakProtocols = ptrBool(false)

	if conf := renderUPSDConf(server); strings.Contains(conf, "DISABLE_WEAK_SSL") {
		t.Fatalf("upsd.conf should not pin the minimum protocol version when opted out, got:\n%s", conf)
	}
}

// A mode of Required with nothing to serve is not TLS. Rendering CERTFILE against a certificate
// that was never supplied would leave upsd unable to start at all.
func TestRenderUPSDConfEmitsNoTLSDirectivesWithoutACertificate(t *testing.T) {
	for name, mutate := range map[string]func(*powerv1alpha1.NUTServer){
		"disabled": func(s *powerv1alpha1.NUTServer) {
			s.Spec.TLS = powerv1alpha1.NUTTLSSpec{Mode: powerv1alpha1.NUTTLSDisabled}
		},
		"no certificate reference": func(s *powerv1alpha1.NUTServer) {
			s.Spec.TLS.ServerCertificateRef = nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			server := tlsEnabledNUTServer()
			mutate(server)

			conf := renderUPSDConf(server)
			if conf != "LISTEN 0.0.0.0 3493\n" {
				t.Fatalf("expected LISTEN-only upsd.conf, got:\n%s", conf)
			}
		})
	}
}

func TestApplyNUTServerTLSOperandAssemblesCombinedCertificate(t *testing.T) {
	server := tlsEnabledNUTServer()
	deployment := &appsv1.Deployment{}
	deployment.Spec.Template.Spec.Containers = []corev1.Container{{Name: "upsd"}}

	applyNUTServerTLSOperand(deployment, server, "ghcr.io/example/nut-server:v1", corev1.PullIfNotPresent)

	podSpec := deployment.Spec.Template.Spec
	if len(podSpec.InitContainers) != 1 {
		t.Fatalf("expected one init container, got %d", len(podSpec.InitContainers))
	}
	init := podSpec.InitContainers[0]
	if init.Image != "ghcr.io/example/nut-server:v1" {
		t.Fatalf("init container should reuse the upsd image, got %q", init.Image)
	}
	script := strings.Join(init.Command, " ")
	// NUT documents CERTFILE as chain-first, key-last. Reversing this works by accident on
	// some OpenSSL versions and not at all on NSS builds, so the order is asserted directly.
	certIndex := strings.Index(script, nutServerTLSCertificateKey)
	keyIndex := strings.Index(script, nutServerTLSPrivateKeyKey)
	if certIndex < 0 || keyIndex < 0 || certIndex > keyIndex {
		t.Fatalf("init container must concatenate certificate before private key, got: %s", script)
	}
	if !strings.Contains(script, nutServerCombinedCertPath) {
		t.Fatalf("init container must write %s, got: %s", nutServerCombinedCertPath, script)
	}

	if !hasVolumeMount(podSpec.Containers[0].VolumeMounts, nutServerCombinedTLSVolume, nutServerCombinedCertDir) {
		t.Fatalf("upsd must mount the assembled certificate directory, got %+v", podSpec.Containers[0].VolumeMounts)
	}
	if hasVolumeMount(podSpec.Containers[0].VolumeMounts, nutServerClientCAVolumeName, nutServerClientCAMountPath) {
		t.Fatalf("client CA must not be mounted when client verification is off")
	}
}

func TestApplyNUTServerTLSOperandSkipsEverythingWhenDisabled(t *testing.T) {
	server := tlsEnabledNUTServer()
	server.Spec.TLS = powerv1alpha1.NUTTLSSpec{Mode: powerv1alpha1.NUTTLSDisabled}
	deployment := &appsv1.Deployment{}
	deployment.Spec.Template.Spec.Containers = []corev1.Container{{Name: "upsd"}}

	applyNUTServerTLSOperand(deployment, server, "ghcr.io/example/nut-server:v1", corev1.PullIfNotPresent)

	if len(deployment.Spec.Template.Spec.InitContainers) != 0 || len(deployment.Spec.Template.Spec.Volumes) != 0 {
		t.Fatalf("disabled TLS must add nothing to the pod spec, got %+v", deployment.Spec.Template.Spec)
	}
}

func hasVolumeMount(mounts []corev1.VolumeMount, name, path string) bool {
	for _, mount := range mounts {
		if mount.Name == name && mount.MountPath == path {
			return true
		}
	}
	return false
}

func TestValidateNUTServerTLSSecretsRejectsIncompleteSecrets(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("register corev1: %v", err)
	}

	for name, testCase := range map[string]struct {
		secret  *corev1.Secret
		wantErr string
	}{
		"missing private key": {
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{Namespace: "power-system", Name: "rack-a-nut-server-tls"},
				Data:       map[string][]byte{nutServerTLSCertificateKey: []byte("cert")},
			},
			wantErr: `data["tls.key"]`,
		},
		"absent secret": {
			wantErr: "get Secret power-system/rack-a-nut-server-tls",
		},
	} {
		t.Run(name, func(t *testing.T) {
			builder := fake.NewClientBuilder().WithScheme(scheme)
			if testCase.secret != nil {
				builder = builder.WithObjects(testCase.secret)
			}
			reconciler := &NUTServerReconciler{Client: builder.Build(), Scheme: scheme}

			err := reconciler.validateNUTServerTLSSecrets(context.Background(), tlsEnabledNUTServer(), "power-system")
			if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("expected error containing %q, got %v", testCase.wantErr, err)
			}
		})
	}
}

func TestValidateNUTServerTLSSecretsRejectsCrossNamespaceRef(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("register corev1: %v", err)
	}
	server := tlsEnabledNUTServer()
	server.Spec.TLS.ServerCertificateRef.Namespace = "elsewhere"
	reconciler := &NUTServerReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).Build(), Scheme: scheme}

	err := reconciler.validateNUTServerTLSSecrets(context.Background(), server, "power-system")
	if err == nil || !strings.Contains(err.Error(), "operand namespace") {
		t.Fatalf("expected a cross-namespace rejection, got %v", err)
	}
}

func monitorTargets(modes ...powerv1alpha1.NUTTLSMode) []agentMonitorTarget {
	targets := make([]agentMonitorTarget, 0, len(modes))
	for _, mode := range modes {
		targets = append(targets, agentMonitorTarget{
			UPSName:   "ups",
			ServerDNS: "rack-a.power-system.svc.cluster.local",
			Port:      3493,
			Username:  "monitor",
			Password:  "secret",
			TLSMode:   mode,
		})
	}
	return targets
}

func TestDeriveAgentTLSPostureRequiresAndVerifiesWhenEveryServerIsRequired(t *testing.T) {
	targets := monitorTargets(powerv1alpha1.NUTTLSRequired, powerv1alpha1.NUTTLSRequired)
	targets[0].ServerCA = []byte("-----BEGIN CERTIFICATE-----\nrack-a\n-----END CERTIFICATE-----")
	targets[1].ServerCA = []byte("-----BEGIN CERTIFICATE-----\nrack-b\n-----END CERTIFICATE-----\n")

	posture := deriveAgentTLSPosture(targets)
	if !posture.ForceSSL || !posture.CertVerify {
		t.Fatalf("expected FORCESSL and CERTVERIFY, got %+v", posture)
	}
	if posture.DowngradeReason != "" {
		t.Fatalf("expected no downgrade, got %q", posture.DowngradeReason)
	}
	bundle := string(posture.CABundle)
	if !strings.Contains(bundle, "rack-a") || !strings.Contains(bundle, "rack-b") {
		t.Fatalf("CA bundle must concatenate every distinct server CA, got:\n%s", bundle)
	}
	// A missing separator would glue the END and BEGIN lines together and invalidate the bundle.
	if strings.Contains(bundle, "-----END CERTIFICATE----------BEGIN CERTIFICATE-----") {
		t.Fatalf("concatenated CAs must stay newline separated, got:\n%s", bundle)
	}
}

func TestDeriveAgentTLSPostureDeduplicatesSharedCAs(t *testing.T) {
	targets := monitorTargets(powerv1alpha1.NUTTLSRequired, powerv1alpha1.NUTTLSRequired)
	targets[0].ServerCA = []byte("shared\n")
	targets[1].ServerCA = []byte("shared\n")

	if got := string(deriveAgentTLSPosture(targets).CABundle); got != "shared\n" {
		t.Fatalf("expected a single copy of a shared CA, got %q", got)
	}
}

// upsmon applies FORCESSL to every MONITOR line, so one lax server would be cut off entirely if
// the strictest server's posture won. Reporting the downgrade is the alternative to silently
// dropping either monitoring or encryption.
func TestDeriveAgentTLSPostureDowngradesOnMixedModes(t *testing.T) {
	targets := monitorTargets(powerv1alpha1.NUTTLSRequired, powerv1alpha1.NUTTLSOpportunistic)
	targets[0].ServerCA = []byte("ca\n")

	posture := deriveAgentTLSPosture(targets)
	if posture.ForceSSL || posture.CertVerify {
		t.Fatalf("mixed modes must not force TLS globally, got %+v", posture)
	}
	if posture.DowngradeReason == "" {
		t.Fatalf("mixed modes must report a downgrade reason")
	}
}

func TestDeriveAgentTLSPostureEncryptsWithoutVerifyingWhenNoCAIsPublished(t *testing.T) {
	posture := deriveAgentTLSPosture(monitorTargets(powerv1alpha1.NUTTLSRequired))

	if !posture.ForceSSL {
		t.Fatalf("Required must still force TLS without a CA, got %+v", posture)
	}
	if posture.CertVerify {
		t.Fatalf("CERTVERIFY cannot be set without a CA to verify against, got %+v", posture)
	}
	if posture.DowngradeReason == "" {
		t.Fatalf("an unauthenticated channel must report a downgrade reason")
	}
}

func TestDeriveAgentTLSPostureStaysEmptyWhenNoServerOffersTLS(t *testing.T) {
	if posture := deriveAgentTLSPosture(monitorTargets(powerv1alpha1.NUTTLSDisabled)); posture.enabled() {
		t.Fatalf("expected an empty posture, got %+v", posture)
	}
	if posture := deriveAgentTLSPosture(nil); posture.enabled() {
		t.Fatalf("expected an empty posture for no targets, got %+v", posture)
	}
}

func TestRenderNodePowerAgentSecretEmitsTLSDirectives(t *testing.T) {
	agent := &powerv1alpha1.NodePowerAgent{ObjectMeta: metav1.ObjectMeta{Name: "edge"}}
	targets := monitorTargets(powerv1alpha1.NUTTLSRequired)
	targets[0].ServerCA = []byte("ca\n")

	data, err := renderNodePowerAgentSecret(agent, targets, deriveAgentTLSPosture(targets))
	if err != nil {
		t.Fatalf("renderNodePowerAgentSecret returned error: %v", err)
	}

	conf := string(data[upsmonConfigFile])
	for _, want := range []string{
		"CERTPATH " + nodePowerAgentServerCAPath + "\n",
		"CERTVERIFY 1\n",
		"FORCESSL 1\n",
	} {
		if !strings.Contains(conf, want) {
			t.Fatalf("upsmon.conf missing %q, got:\n%s", want, conf)
		}
	}
	if got := string(data[nodePowerAgentServerCAFile]); got != "ca\n" {
		t.Fatalf("expected the CA bundle alongside upsmon.conf, got %q", got)
	}
}

// NUT compiles the SSL directives in conditionally, so an upsmon image built without SSL support
// would fail to parse a config that names them. A plaintext deployment must render exactly what
// it rendered before TLS support existed.
func TestRenderNodePowerAgentSecretOmitsTLSDirectivesWithoutTLS(t *testing.T) {
	agent := &powerv1alpha1.NodePowerAgent{ObjectMeta: metav1.ObjectMeta{Name: "edge"}}
	targets := monitorTargets(powerv1alpha1.NUTTLSDisabled)

	data, err := renderNodePowerAgentSecret(agent, targets, deriveAgentTLSPosture(targets))
	if err != nil {
		t.Fatalf("renderNodePowerAgentSecret returned error: %v", err)
	}

	conf := string(data[upsmonConfigFile])
	for _, unwanted := range []string{"CERTPATH", "CERTVERIFY", "FORCESSL"} {
		if strings.Contains(conf, unwanted) {
			t.Fatalf("upsmon.conf must not mention %q without TLS, got:\n%s", unwanted, conf)
		}
	}
	if _, present := data[nodePowerAgentServerCAFile]; present {
		t.Fatalf("no CA file should be written without TLS")
	}
}

func TestMonitoredServerTLSModeReportsDisabledWithoutACertificate(t *testing.T) {
	server := tlsEnabledNUTServer()
	server.Spec.TLS.ServerCertificateRef = nil

	if got := monitoredServerTLSMode(server); got != powerv1alpha1.NUTTLSDisabled {
		t.Fatalf("a Required server with no certificate cannot serve TLS, got %q", got)
	}
}
