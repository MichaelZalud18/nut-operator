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
	"crypto/rand"
	"encoding/base64"
	"fmt"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// resolveUPSDeviceCredentials fetches spec.credentialSecretRef for each selected device (SNMP
// community, SNMPv3 secName/authPassword/privPassword, etc.) so renderUPSConf can merge real
// driver auth fields into ups.conf instead of leaving them unwired. Same-namespace-only, matching
// the existing upstreamNUTAuthProjections convention.
func resolveUPSDeviceCredentials(ctx context.Context, c client.Client, namespace string, devices []powerv1alpha1.UPSDevice) (map[string]map[string]string, error) {
	credentials := make(map[string]map[string]string)
	for _, device := range devices {
		ref := device.Spec.CredentialSecretRef
		if ref == nil {
			continue
		}
		if ref.Namespace != namespace {
			return nil, fmt.Errorf("UPSDevice %q credentialSecretRef must be in operand namespace %q", device.Name, namespace)
		}
		var secret corev1.Secret
		if err := c.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, &secret); err != nil {
			return nil, fmt.Errorf("get credentialSecretRef Secret for UPSDevice %q: %w", device.Name, err)
		}
		values := make(map[string]string, len(secret.Data))
		for key, value := range secret.Data {
			values[key] = string(value)
		}
		credentials[device.Name] = values
	}
	return credentials, nil
}

func (r *NUTServerReconciler) ensureNUTServerDriverConfigSecret(ctx context.Context, server *powerv1alpha1.NUTServer, namespace, upsConf string) (*corev1.Secret, error) {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: driverConfigSecretName(server), Namespace: namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		secret.Labels = labelsForNUTServer(server)
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = map[string][]byte{"ups.conf": []byte(upsConf)}
		return controllerutil.SetControllerReference(server, secret, r.Scheme)
	})
	return secret, err
}

func (r *NUTServerReconciler) ensureNUTUsersSecret(ctx context.Context, server *powerv1alpha1.NUTServer, namespace string) (powerv1alpha1.NamespacedNameReference, string, error) {
	if server.Spec.Auth.Mode == powerv1alpha1.NUTAuthExistingSecret && server.Spec.Auth.ExistingSecretRef != nil {
		if server.Spec.Auth.ExistingSecretRef.Namespace != namespace {
			return powerv1alpha1.NamespacedNameReference{}, "", fmt.Errorf("ExistingSecret auth for NUTServer %q must reference a Secret in operand namespace %q", server.Name, namespace)
		}
		return *server.Spec.Auth.ExistingSecretRef, "", nil
	}

	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: operatorManagedUsersSecretName(server), Namespace: namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		secret.Labels = labelsForNUTServer(server)
		secret.Type = corev1.SecretTypeOpaque
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		if len(secret.Data["admin-password"]) == 0 {
			password, err := randomPassword()
			if err != nil {
				return err
			}
			secret.Data["admin-password"] = []byte(password)
		}
		if len(secret.Data["monitor-password"]) == 0 {
			password, err := randomPassword()
			if err != nil {
				return err
			}
			secret.Data["monitor-password"] = []byte(password)
		}
		secret.Data["upsd.users"] = []byte(renderUPSDUsers(
			string(secret.Data["admin-password"]),
			string(secret.Data["monitor-password"]),
		))
		return controllerutil.SetControllerReference(server, secret, r.Scheme)
	})
	if err != nil {
		return powerv1alpha1.NamespacedNameReference{}, "", err
	}
	return powerv1alpha1.NamespacedNameReference{Namespace: namespace, Name: secret.Name}, secret.Name, nil
}

func renderUPSDUsers(adminPassword, monitorPassword string) string {
	return fmt.Sprintf(`[admin]
  password = %s
  actions = SET
  instcmds = ALL

[monitor]
  password = %s
  upsmon secondary
`, adminPassword, monitorPassword)
}

func randomPassword() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate operator-managed NUT credential: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}
