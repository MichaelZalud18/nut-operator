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
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
)

// nutServerTLSEnabled reports whether upsd should be configured to offer TLS at all. Both
// Required and Opportunistic serve the same certificate; the modes differ in what clients are
// told to accept, which is an upsmon.conf concern rather than an upsd.conf one.
func nutServerTLSEnabled(server *powerv1alpha1.NUTServer) bool {
	return server.Spec.TLS.Mode != powerv1alpha1.NUTTLSDisabled && server.Spec.TLS.ServerCertificateRef != nil
}

func nutServerVerifiesClientCertificates(server *powerv1alpha1.NUTServer) bool {
	return server.Spec.TLS.VerifyClientCertificates != nil &&
		*server.Spec.TLS.VerifyClientCertificates &&
		server.Spec.TLS.ClientCARef != nil
}

func nutServerDisableWeakProtocols(server *powerv1alpha1.NUTServer) bool {
	return server.Spec.TLS.DisableWeakProtocols == nil || *server.Spec.TLS.DisableWeakProtocols
}

// validateNUTServerTLSSecrets fails the reconcile before the Deployment is written when the
// referenced TLS Secrets cannot produce a working upsd. Without it the only symptom is an init
// container that crash-loops on a missing file, or worse, an upsd that starts and quietly falls
// back to plaintext, which is exactly the failure this whole code path exists to prevent.
func (r *NUTServerReconciler) validateNUTServerTLSSecrets(ctx context.Context, server *powerv1alpha1.NUTServer, namespace string) error {
	if !nutServerTLSEnabled(server) {
		return nil
	}
	if err := r.requireSecretKeys(ctx, namespace, *server.Spec.TLS.ServerCertificateRef, "spec.tls.serverCertificateRef",
		nutServerTLSCertificateKey, nutServerTLSPrivateKeyKey); err != nil {
		return err
	}
	if !nutServerVerifiesClientCertificates(server) {
		return nil
	}
	return r.requireSecretKeys(ctx, namespace, *server.Spec.TLS.ClientCARef, "spec.tls.clientCARef", nutServerCABundleKey)
}

// nutServerTLSMaterialDigest summarizes the certificate material upsd serves, for the restart hash.
//
// The certificate is not part of the rendered config: it arrives through a referenced Secret and is
// mounted as a volume, so a rotation changes the pod's filesystem without changing anything the
// operator writes. upsd builds its SSL context once at startup, so the rotated certificate sits on
// disk unserved until the process restarts. Folding a digest of the material into the restart hash
// is what turns a rotation into a rollout.
//
// It is a digest rather than the material itself because the value ends up in a
// pod-template annotation, which is broadly readable, and a SHA-256 over the certificate and key
// cannot be reversed to recover them.
func (r *NUTServerReconciler) nutServerTLSMaterialDigest(ctx context.Context, server *powerv1alpha1.NUTServer, namespace string) (string, error) {
	if !nutServerTLSEnabled(server) {
		return "", nil
	}

	material := map[string][]byte{}
	var serverSecret corev1.Secret
	key := types.NamespacedName{Namespace: namespace, Name: server.Spec.TLS.ServerCertificateRef.Name}
	if err := r.Get(ctx, key, &serverSecret); err != nil {
		return "", fmt.Errorf("get Secret %s for spec.tls.serverCertificateRef: %w", key, err)
	}
	material[nutServerTLSCertificateKey] = serverSecret.Data[nutServerTLSCertificateKey]
	material[nutServerTLSPrivateKeyKey] = serverSecret.Data[nutServerTLSPrivateKeyKey]

	if nutServerVerifiesClientCertificates(server) {
		var caSecret corev1.Secret
		caKey := types.NamespacedName{Namespace: namespace, Name: server.Spec.TLS.ClientCARef.Name}
		if err := r.Get(ctx, caKey, &caSecret); err != nil {
			return "", fmt.Errorf("get Secret %s for spec.tls.clientCARef: %w", caKey, err)
		}
		material["clientCA/"+nutServerCABundleKey] = caSecret.Data[nutServerCABundleKey]
	}
	return hashByteMap(material), nil
}

// requireSecretKeys enforces both that the Secret carries the expected keys and that it lives in
// the operand namespace. The namespace check is not redundant with the webhook: a pod can only
// mount Secrets from its own namespace, so a ref naming another namespace would otherwise render
// a volume silently pointed at a same-named Secret that may not exist or may be someone else's.
func (r *NUTServerReconciler) requireSecretKeys(ctx context.Context, namespace string, ref powerv1alpha1.NamespacedNameReference, fieldPath string, keys ...string) error {
	if ref.Namespace != "" && ref.Namespace != namespace {
		return fmt.Errorf("%s must name a Secret in operand namespace %q, got %q", fieldPath, namespace, ref.Namespace)
	}
	var secret corev1.Secret
	if err := r.Get(ctx, types.NamespacedName{Namespace: namespace, Name: ref.Name}, &secret); err != nil {
		return fmt.Errorf("get Secret %s/%s for %s: %w", namespace, ref.Name, fieldPath, err)
	}
	for _, key := range keys {
		if len(secret.Data[key]) == 0 {
			return fmt.Errorf("secret %s/%s for %s requires a non-empty data[%q]", namespace, ref.Name, fieldPath, key)
		}
	}
	return nil
}

// applyNUTServerTLSOperand attaches everything upsd needs to actually terminate TLS: the
// certificate Secret, an init container that concatenates it into CERTFILE's expected
// chain-then-key layout, and the client CA when certificate-based client validation is on.
//
// The concatenation is an init container rather than an entrypoint change because the upsd image
// is caller-supplied (spec.image) and the operator does not control its entrypoint. It reuses the
// same image so no second image has to be pulled or trusted, and writes to an emptyDir because
// the operand runs with a read-only root filesystem.
func applyNUTServerTLSOperand(deployment *appsv1.Deployment, server *powerv1alpha1.NUTServer, image string, pullPolicy corev1.PullPolicy) {
	if !nutServerTLSEnabled(server) {
		return
	}
	podSpec := &deployment.Spec.Template.Spec

	podSpec.Volumes = append(podSpec.Volumes,
		corev1.Volume{
			Name: nutServerTLSVolumeName,
			VolumeSource: corev1.VolumeSource{
				Secret: &corev1.SecretVolumeSource{
					SecretName:  server.Spec.TLS.ServerCertificateRef.Name,
					DefaultMode: ptrInt32(0440),
				},
			},
		},
		corev1.Volume{
			Name: nutServerCombinedTLSVolume,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{
					Medium:    corev1.StorageMediumMemory,
					SizeLimit: resource.NewQuantity(1*1024*1024, resource.BinarySI),
				},
			},
		},
	)
	podSpec.InitContainers = append(podSpec.InitContainers, corev1.Container{
		Name:            nutServerTLSInitContainerName,
		Image:           image,
		ImagePullPolicy: pullPolicy,
		Command:         []string{"sh", "-c", nutServerTLSAssembleScript()},
		SecurityContext: &corev1.SecurityContext{
			AllowPrivilegeEscalation: ptrBool(false),
			ReadOnlyRootFilesystem:   ptrBool(true),
			Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
		},
		VolumeMounts: []corev1.VolumeMount{
			{Name: nutServerTLSVolumeName, MountPath: nutServerCertificateMountPath, ReadOnly: true},
			{Name: nutServerCombinedTLSVolume, MountPath: nutServerCombinedCertDir},
		},
	})
	upsd := nutServerUpsdContainer(podSpec)
	if upsd == nil {
		return
	}
	upsd.VolumeMounts = append(upsd.VolumeMounts,
		corev1.VolumeMount{Name: nutServerCombinedTLSVolume, MountPath: nutServerCombinedCertDir, ReadOnly: true},
	)

	if !nutServerVerifiesClientCertificates(server) {
		return
	}
	podSpec.Volumes = append(podSpec.Volumes, corev1.Volume{
		Name: nutServerClientCAVolumeName,
		VolumeSource: corev1.VolumeSource{
			Secret: &corev1.SecretVolumeSource{
				SecretName:  server.Spec.TLS.ClientCARef.Name,
				DefaultMode: ptrInt32(0440),
			},
		},
	})
	upsd.VolumeMounts = append(upsd.VolumeMounts,
		corev1.VolumeMount{Name: nutServerClientCAVolumeName, MountPath: nutServerClientCAMountPath, ReadOnly: true},
	)
}

// nutServerUpsdContainer finds the server container by name rather than by position.
//
// TLS mounts must target upsd even if the container slice is reordered; mounting the
// certificate and CA into the supervisor would leave the protocol server without them.
func nutServerUpsdContainer(podSpec *corev1.PodSpec) *corev1.Container {
	for i := range podSpec.Containers {
		if podSpec.Containers[i].Name == nutServerUpsdContainerName {
			return &podSpec.Containers[i]
		}
	}
	return nil
}

// nutServerTLSAssembleScript writes the CERTFILE upsd expects. Order matters: NUT documents the
// subject certificate first, then any intermediates, then the matching private key last.
func nutServerTLSAssembleScript() string {
	return fmt.Sprintf(
		"set -e; umask 077; cat %s/%s %s/%s > %s",
		nutServerCertificateMountPath, nutServerTLSCertificateKey,
		nutServerCertificateMountPath, nutServerTLSPrivateKeyKey,
		nutServerCombinedCertPath,
	)
}
