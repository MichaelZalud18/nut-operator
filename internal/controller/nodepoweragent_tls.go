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
	"fmt"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
)

// agentTLSPosture is the upsmon-side TLS configuration derived from the NUTServers this agent
// monitors. CERTVERIFY and FORCESSL are process-global in upsmon (per-host overrides need
// CERTHOST, NUT 2.8.5+), so an agent monitoring servers with different spec.tls.mode values has
// to settle on the weakest common posture or it would break the laxer servers outright.
type agentTLSPosture struct {
	CABundle   []byte
	ForceSSL   bool
	CertVerify bool
	// DowngradeReason is empty when the rendered posture is as strict as the monitored servers
	// asked for. When set it names why upsmon was configured more loosely than spec.tls.mode
	// implies, so the caller can surface it instead of silently under-securing the link.
	DowngradeReason string
}

func (p agentTLSPosture) enabled() bool {
	return p.ForceSSL || p.CertVerify || len(p.CABundle) > 0
}

// deriveAgentTLSPosture folds the monitored servers' TLS modes into one upsmon configuration.
func deriveAgentTLSPosture(targets []agentMonitorTarget) agentTLSPosture {
	var posture agentTLSPosture
	if len(targets) == 0 {
		return posture
	}

	seen := map[string]struct{}{}
	anyRequired := false
	allRequired := true
	anyTLS := false
	for _, target := range targets {
		switch target.TLSMode {
		case powerv1alpha1.NUTTLSRequired:
			anyRequired = true
			anyTLS = true
		case powerv1alpha1.NUTTLSOpportunistic:
			allRequired = false
			anyTLS = true
		default:
			allRequired = false
		}
		if len(target.ServerCA) == 0 {
			continue
		}
		if _, duplicate := seen[string(target.ServerCA)]; duplicate {
			continue
		}
		seen[string(target.ServerCA)] = struct{}{}
		posture.CABundle = append(posture.CABundle, target.ServerCA...)
		if len(target.ServerCA) > 0 && target.ServerCA[len(target.ServerCA)-1] != '\n' {
			posture.CABundle = append(posture.CABundle, '\n')
		}
	}

	if !anyTLS {
		return agentTLSPosture{}
	}
	switch {
	case anyRequired && !allRequired:
		posture.DowngradeReason = "one or more monitored NUTServers do not set spec.tls.mode Required, " +
			"and upsmon applies FORCESSL to every MONITOR line"
	case allRequired && len(posture.CABundle) == 0:
		posture.ForceSSL = true
		posture.DowngradeReason = "no monitored NUTServer sets spec.tls.serverCARef, so upsmon encrypts " +
			"but cannot authenticate the server certificate"
	case allRequired:
		posture.ForceSSL = true
		posture.CertVerify = true
	}
	return posture
}

func (r *NodePowerAgentReconciler) monitorPassword(ctx context.Context, server *powerv1alpha1.NUTServer, namespace string) (string, error) {
	secretRef := powerv1alpha1.NamespacedNameReference{Namespace: namespace, Name: operatorManagedUsersSecretName(server)}
	if server.Spec.Auth.Mode == powerv1alpha1.NUTAuthExistingSecret && server.Spec.Auth.ExistingSecretRef != nil {
		secretRef = *server.Spec.Auth.ExistingSecretRef
	}
	if secretRef.Namespace != namespace {
		return "", fmt.Errorf("NUTServer %q monitor Secret must be in operand namespace %q", server.Name, namespace)
	}

	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: secretRef.Namespace, Name: secretRef.Name}, &secret); err != nil {
		return "", fmt.Errorf("get monitor Secret %s/%s for NUTServer %q: %w", secretRef.Namespace, secretRef.Name, server.Name, err)
	}
	password := string(secret.Data["monitor-password"])
	if password == "" {
		return "", fmt.Errorf("monitor Secret %s/%s for NUTServer %q requires data[monitor-password]", secretRef.Namespace, secretRef.Name, server.Name)
	}
	return password, nil
}

// monitoredServerTLSMode reports the TLS posture upsd will actually present. A mode of Required
// with no certificate to serve is a server that cannot terminate TLS, so it is reported as
// Disabled rather than taken at its word — otherwise the agent would render FORCESSL against a
// plaintext server and refuse to monitor it at all.
func monitoredServerTLSMode(server *powerv1alpha1.NUTServer) powerv1alpha1.NUTTLSMode {
	if !nutServerTLSEnabled(server) {
		return powerv1alpha1.NUTTLSDisabled
	}
	return server.Spec.TLS.Mode
}

// serverCABundle loads the CA that upsmon should trust for this server's certificate.
func (r *NodePowerAgentReconciler) serverCABundle(ctx context.Context, server *powerv1alpha1.NUTServer, namespace string) ([]byte, error) {
	ref := server.Spec.TLS.ServerCARef
	if ref == nil || !nutServerTLSEnabled(server) {
		return nil, nil
	}
	if ref.Namespace != "" && ref.Namespace != namespace {
		return nil, fmt.Errorf("NUTServer %q spec.tls.serverCARef must name a Secret in operand namespace %q, got %q", server.Name, namespace, ref.Namespace)
	}
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ref.Name}, &secret); err != nil {
		return nil, fmt.Errorf("get Secret %s/%s for NUTServer %q spec.tls.serverCARef: %w", namespace, ref.Name, server.Name, err)
	}
	bundle := secret.Data[nutServerCABundleKey]
	if len(bundle) == 0 {
		return nil, fmt.Errorf("secret %s/%s for NUTServer %q spec.tls.serverCARef requires a non-empty data[%q]", namespace, ref.Name, server.Name, nutServerCABundleKey)
	}
	return bundle, nil
}

// nodePowerAgentServerCARehashScript builds the CApath upsmon needs.
//
// openssl rehash writes the <subject-hash>.N symlinks OpenSSL looks for when it
// walks a CApath. Without them the directory reads as empty and every certificate
// verification fails, which is the F-40 failure mode. The bundle may hold several
// CAs when an agent monitors several servers, and rehash handles that by splitting
// on subject rather than on file.
func nodePowerAgentServerCARehashScript() string {
	return fmt.Sprintf(
		"set -e; umask 022; cp %s %s/server-ca.pem; openssl rehash %s",
		nodePowerAgentServerCASourcePath,
		nodePowerAgentServerCAPath,
		nodePowerAgentServerCAPath,
	)
}
