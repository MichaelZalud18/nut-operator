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

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/nut"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *UPSDeviceReconciler) telemetryTLS(ctx context.Context, server *power.NUTServer, host string) (nut.TLSOptions, error) {
	options := nut.TLSOptions{Mode: nut.TLSDisabled}
	if !nutServerTLSEnabled(server) {
		return options, nil
	}
	options.Mode = nut.TLSMode(server.Spec.TLS.Mode)
	if options.Mode == "" {
		options.Mode = nut.TLSRequired
	}
	options.ServerName = host
	var cluster *power.PowerManagementCluster
	if server.Spec.Namespace == "" && server.Spec.ManagementClusterRef != nil {
		cluster = &power.PowerManagementCluster{}
		if err := r.Get(ctx, client.ObjectKey{Name: server.Spec.ManagementClusterRef.Name}, cluster); err != nil {
			return options, fmt.Errorf("resolve telemetry TLS operand namespace: %w", err)
		}
	}
	namespace := nutServerNamespace(server, cluster)
	ref := server.Spec.TLS.ServerCARef
	key := nutServerCABundleKey
	if ref == nil {
		// Trust the declared public serving material when no separate CA was supplied;
		// authoritative telemetry never opts out of certificate/hostname verification.
		ref = server.Spec.TLS.ServerCertificateRef
		key = nutServerTLSCertificateKey
	}
	if ref.Namespace != "" && ref.Namespace != namespace {
		return options, fmt.Errorf("NUTServer %q telemetry TLS Secret must be in operand namespace %q", server.Name, namespace)
	}
	var secret corev1.Secret
	if err := r.Get(ctx, client.ObjectKey{Namespace: namespace, Name: ref.Name}, &secret); err != nil {
		return options, fmt.Errorf("read NUTServer %q telemetry TLS trust: %w", server.Name, err)
	}
	if len(secret.Data[key]) == 0 {
		return options, fmt.Errorf("NUTServer %q telemetry TLS Secret requires non-empty %s", server.Name, key)
	}
	options.CABundle = append([]byte(nil), secret.Data[key]...)
	return options, nil
}
