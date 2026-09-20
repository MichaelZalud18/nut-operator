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
	"sort"
	"strings"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func renderNUTServerConfig(server *powerv1alpha1.NUTServer, devices []powerv1alpha1.UPSDevice, credentials map[string]map[string]string, simulationFixtures map[string]string) (map[string]string, error) {
	upsConf, err := renderUPSConf(devices, credentials)
	if err != nil {
		return nil, err
	}

	config := map[string]string{
		"nut.conf":  "MODE=netserver\n",
		"ups.conf":  upsConf,
		"upsd.conf": renderUPSDConf(server),
	}
	for _, device := range devices {
		if filename, ok, err := dummyUPSSimulationFileName(device); err != nil {
			return nil, err
		} else if ok {
			fixture, hasFixture := simulationFixtures[device.Name]
			if !hasFixture {
				return nil, fmt.Errorf("UPSDevice %q simulation.sequenceConfigMapRef resolved no fixture content", device.Name)
			}
			config[filename] = fixture
			continue
		}
		filename, ok, err := dummyUPSDefinitionFileName(device)
		if err != nil {
			return nil, err
		}
		if ok {
			definition, err := renderDummyUPSDefinition(device)
			if err != nil {
				return nil, err
			}
			config[filename] = definition
		}
	}
	return config, nil
}

// renderUPSDConf emits upsd.conf. LISTEN alone only opens a plaintext socket: upsd negotiates
// STARTTLS solely when CERTFILE (OpenSSL builds) names a usable key pair, so a spec.tls block
// that mounts a certificate without emitting CERTFILE serves cleartext NUT — including the
// MONITOR password every upsmon sends on connect.
func renderUPSDConf(server *powerv1alpha1.NUTServer) string {
	var out strings.Builder
	fmt.Fprintf(&out, "LISTEN %s %d\n", listenAddress(server), servicePort(server))

	// ALLOW_NO_DEVICE is rendered unconditionally, not only when the selector currently matches
	// nothing.
	//
	// Without it upsd calls fatalx on a device-less ups.conf -- verified: "Fatal error: at least
	// one UPS must be defined in ups.conf", exit 1 -- so a NUTServer whose selector matches nothing
	// yet, or stops matching, cannot start. That turns an ordinary empty state into a crash loop.
	//
	// Rendering it conditionally would be worse than rendering it always: the directive would then
	// appear and disappear as devices come and go, so the transition from one device to zero would
	// itself require a config change to survive, which is the situation the directive exists to
	// avoid. Upstream's own message names the intended lifecycle -- "please configure the file and
	// reload the service".
	//
	// Nothing is hidden by this. A server with no responsive driver reports NotReady through NS-1
	// and leaves the Service endpoints, so an empty server is visibly idle rather than silently
	// serving nothing.
	out.WriteString("ALLOW_NO_DEVICE true\n")

	if !nutServerTLSEnabled(server) {
		return out.String()
	}

	// NUT wants the chain first and the private key last in one file; the init container
	// assembles it because a kubernetes.io/tls Secret projects tls.crt and tls.key separately.
	fmt.Fprintf(&out, "CERTFILE %s\n", nutServerCombinedCertPath)
	if nutServerDisableWeakProtocols(server) {
		out.WriteString("DISABLE_WEAK_SSL true\n")
	}
	if nutServerVerifiesClientCertificates(server) {
		fmt.Fprintf(&out, "CERTPATH %s\n", nutServerClientCAPath)
		out.WriteString("CERTREQUEST 2\n")
	}
	return out.String()
}

func renderUPSConf(devices []powerv1alpha1.UPSDevice, credentials map[string]map[string]string) (string, error) {
	var out strings.Builder
	for _, device := range devices {
		if result := validateUPSDevice(&device); !result.accepted {
			return "", fmt.Errorf("invalid selected UPSDevice %q: %s", device.Name, result.message)
		}

		name := nutDeviceName(device)
		if err := validateNUTConfigToken(name); err != nil {
			return "", fmt.Errorf("invalid NUT device name for UPSDevice %q: %w", device.Name, err)
		}
		driver := renderedUPSDriver(device)
		if err := validateNUTConfigValue(driver); err != nil {
			return "", fmt.Errorf("invalid driver for UPSDevice %q: %w", device.Name, err)
		}

		fmt.Fprintf(&out, "[%s]\n", name)
		fmt.Fprintf(&out, "  driver = %s\n", driver)
		if device.Spec.UpstreamNUT != nil {
			target := upstreamNUTTarget(device)
			if err := validateNUTConfigValue(target); err != nil {
				return "", fmt.Errorf("invalid upstream NUT target for UPSDevice %q: %w", device.Name, err)
			}
			fmt.Fprintf(&out, "  port = %s\n", target)
			fmt.Fprintf(&out, "  mode = repeater\n")
			// NUT 2.8.5 has no authconf option. Validation rejects modes requiring it.
			if !upstreamNUTStrictStart(device) {
				fmt.Fprintf(&out, "  repeater_disable_strict_start = true\n")
			}
		}
		if filename, ok, err := dummyUPSSimulationFileName(device); err != nil {
			return "", err
		} else if ok {
			fmt.Fprintf(&out, "  port = %s\n", filename)
		} else if filename, ok, err := dummyUPSDefinitionFileName(device); err != nil {
			return "", err
		} else if ok {
			fmt.Fprintf(&out, "  port = %s\n", filename)
		}
		if device.Spec.Endpoint != nil {
			endpoint := device.Spec.Endpoint.Host
			if device.Spec.Endpoint.Port != nil {
				endpoint = fmt.Sprintf("%s:%d", endpoint, *device.Spec.Endpoint.Port)
			}
			if err := validateNUTConfigValue(endpoint); err != nil {
				return "", fmt.Errorf("invalid endpoint for UPSDevice %q: %w", device.Name, err)
			}
			if _, hasPort := device.Spec.DriverOptions["port"]; !hasPort {
				fmt.Fprintf(&out, "  port = %s\n", endpoint)
			}
		}

		options := make(map[string]string, len(device.Spec.DriverOptions)+1)
		if hasSimulationSequence(device) {
			// Explicit, even though the .seq extension alone already selects dummy-loop mode
			// (belt-and-suspenders); a user-supplied mode below still wins on key collision.
			options["mode"] = "dummy-loop"
		}
		for key, value := range device.Spec.DriverOptions {
			options[key] = value
		}
		// Secrets may override authentication options, never operator-owned connection fields.
		// Device admission cannot validate this separately mutable Secret content.
		for key, value := range credentials[device.Name] {
			switch strings.ToLower(key) {
			case "driver", "port", "mode", "authconf", "repeater_disable_strict_start":
				return "", fmt.Errorf("credential Secret for UPSDevice %q contains reserved driver option %q", device.Name, key)
			}
			options[key] = value
		}

		keys := make([]string, 0, len(options))
		for key := range options {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value := options[key]
			if err := validateNUTConfigToken(key); err != nil {
				return "", fmt.Errorf("invalid driver option key for UPSDevice %q: %w", device.Name, err)
			}
			if err := validateNUTConfigValue(value); err != nil {
				return "", fmt.Errorf("invalid driver option value for UPSDevice %q: %w", device.Name, err)
			}
			fmt.Fprintf(&out, "  %s = %s\n", key, value)
		}
		out.WriteString("\n")
	}
	return out.String(), nil
}

func nutDeviceName(device powerv1alpha1.UPSDevice) string {
	if device.Status.NUTName != "" {
		return sanitizeNUTName(device.Status.NUTName)
	}
	return sanitizeNUTName(device.Name)
}

func sanitizeNUTName(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func validateNUTConfigToken(value string) error {
	if value == "" {
		return fmt.Errorf("value is empty")
	}
	return validateNUTConfigValue(value)
}

func validateNUTConfigValue(value string) error {
	if strings.ContainsAny(value, "\r\n[]") {
		return fmt.Errorf("value contains unsupported control or section characters")
	}
	return nil
}

func listenAddress(server *powerv1alpha1.NUTServer) string {
	if server.Spec.Config.ListenAddress != "" {
		return server.Spec.Config.ListenAddress
	}
	return "0.0.0.0"
}

func servicePort(server *powerv1alpha1.NUTServer) int32 {
	if server.Spec.Service.Port != nil {
		return *server.Spec.Service.Port
	}
	return 3493
}

func (r *NUTServerReconciler) ensureNUTServerConfigMap(ctx context.Context, server *powerv1alpha1.NUTServer, namespace string, data map[string]string) (*corev1.ConfigMap, error) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: configMapName(server), Namespace: namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		cm.Labels = labelsForNUTServer(server)
		cm.Data = data
		return controllerutil.SetControllerReference(server, cm, r.Scheme)
	})
	return cm, err
}
