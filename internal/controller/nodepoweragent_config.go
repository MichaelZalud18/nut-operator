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
	"strings"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func renderNodePowerAgentConfig() map[string]string {
	return map[string]string{
		nodePowerAgentConfigFile: "MODE=netclient\n",
	}
}

func renderNodePowerAgentSecret(agent *powerv1alpha1.NodePowerAgent, targets []agentMonitorTarget, posture agentTLSPosture) (map[string][]byte, error) {
	var out strings.Builder
	out.WriteString("MINSUPPLIES 1\n")
	fmt.Fprintf(&out, "SHUTDOWNCMD %s\n", shellQuotedNUTValue(nodePowerAgentSignalWriterPath))
	fmt.Fprintf(&out, "POLLFREQ %d\n", durationSeconds(agent.Spec.Upsmon.PollFrequency, 15))
	fmt.Fprintf(&out, "POLLFREQALERT %d\n", durationSeconds(agent.Spec.Upsmon.AlertPollFrequency, 5))
	fmt.Fprintf(&out, "HOSTSYNC %d\n", durationSeconds(agent.Spec.Upsmon.HostSync, 15))
	fmt.Fprintf(&out, "DEADTIME %d\n", durationSeconds(agent.Spec.Upsmon.DeadTime, 45))
	// No POWERDOWNFLAG (F-66). upsmon writes that file so the system's own late-boot shutdown
	// scripts can ask "did we power off because of the UPS?" and call `upsdrvctl shutdown`. This
	// operand has no such script and no init system to run one, so the directive named a path
	// nothing would ever read. Rendering an inert NUT directive invites a future reader to assume
	// there is a consumer.
	fmt.Fprintf(&out, "FINALDELAY %d\n", durationSeconds(agent.Spec.Upsmon.FinalDelay, 10))

	// The notification surface (F-68). Without NOTIFYCMD upsmon does not stay quiet -- it falls back
	// to `wall`, which the operand image does not ship, so every notification logged
	// "Warning: no custom notification command defined" followed by "sh: wall: not found". The
	// notifications were not merely unused; they were failing.
	//
	// EXEC rather than SYSLOG on the communication events, because these are the ones something
	// else needs to read: COMMOK/COMMBAD/NOCOMM are how a node reports whether it still holds a
	// working session with its server, which is the check F-65's readiness probe cannot make from
	// upsc alone. The writer records them to a file; nothing here interprets them.
	fmt.Fprintf(&out, "NOTIFYCMD %s\n", shellQuotedNUTValue(nodePowerAgentNotifyWriterPath))
	for _, event := range nodePowerAgentNotifyEvents {
		fmt.Fprintf(&out, "NOTIFYFLAG %s SYSLOG+EXEC\n", event)
	}
	fmt.Fprintf(&out, "NOCOMMWARNTIME %d\n", durationSeconds(agent.Spec.Upsmon.NoCommWarnTime, 300))
	fmt.Fprintf(&out, "RBWARNTIME %d\n", durationSeconds(agent.Spec.Upsmon.ReplaceBatteryWarnTime, 43200))

	for _, target := range targets {
		serverAddress := target.ServerDNS
		if target.Port != 3493 {
			serverAddress = fmt.Sprintf("%s:%d", target.ServerDNS, target.Port)
		}
		system := fmt.Sprintf("%s@%s", target.UPSName, serverAddress)
		if err := validateNUTConfigToken(target.UPSName); err != nil {
			return nil, fmt.Errorf("invalid UPS monitor target name %q: %w", target.UPSName, err)
		}
		if err := validateNUTConfigValue(serverAddress); err != nil {
			return nil, fmt.Errorf("invalid UPS monitor server %q: %w", serverAddress, err)
		}
		if err := validateNUTConfigValue(target.Username); err != nil {
			return nil, fmt.Errorf("invalid UPS monitor username: %w", err)
		}
		if err := validateNUTConfigValue(target.Password); err != nil {
			return nil, fmt.Errorf("invalid UPS monitor password: %w", err)
		}
		fmt.Fprintf(&out, "MONITOR %s 1 %s %s secondary\n", system, target.Username, target.Password)
	}

	// The TLS block is emitted only when at least one monitored server actually offers TLS. NUT
	// compiles CERTPATH/CERTVERIFY/FORCESSL in conditionally (WITH_SSL), so writing them
	// unconditionally would hand a parse error to any upsmon image built without SSL support —
	// including every deployment that legitimately runs spec.tls.mode Disabled.
	if posture.enabled() {
		if len(posture.CABundle) > 0 {
			fmt.Fprintf(&out, "CERTPATH %s\n", nodePowerAgentServerCAPath)
		}
		fmt.Fprintf(&out, "CERTVERIFY %s\n", nutBoolean(posture.CertVerify))
		fmt.Fprintf(&out, "FORCESSL %s\n", nutBoolean(posture.ForceSSL))
	}

	data := map[string][]byte{
		upsmonConfigFile: []byte(out.String()),
	}
	if len(posture.CABundle) > 0 {
		data[nodePowerAgentServerCAFile] = posture.CABundle
	}
	return data, nil
}

func nutBoolean(value bool) string {
	if value {
		return "1"
	}
	return "0"
}

// nodePowerAgentNotifyEvents are the upsmon events dispatched through NOTIFYCMD (F-68).
//
// The communication events are the load-bearing ones -- they are what say whether this node still
// holds a working session with its server. The power events are included because a node that saw
// ONBATT and then went quiet is a different story from one that never saw it, and that distinction
// is only available if both were recorded.
var nodePowerAgentNotifyEvents = []string{
	"ONLINE", "ONBATT", "LOWBATT", "FSD", "COMMOK", "COMMBAD", "NOCOMM", "REPLBATT", "SHUTDOWN",
}

func nodePowerAgentSignalPath(agent *powerv1alpha1.NodePowerAgent) string {
	if agent.Spec.Shutdown.SignalPath != "" {
		return agent.Spec.Shutdown.SignalPath
	}
	return "/run/power-agent/shutdown.json"
}

func shellQuotedNUTValue(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
}

func (r *NodePowerAgentReconciler) ensureNodePowerAgentConfigMap(ctx context.Context, agent *powerv1alpha1.NodePowerAgent, namespace string, data map[string]string) (*corev1.ConfigMap, error) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: nodePowerAgentConfigMapName(agent), Namespace: namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, cm, func() error {
		cm.Labels = labelsForNodePowerAgent(agent)
		cm.Data = data
		return controllerutil.SetControllerReference(agent, cm, r.Scheme)
	})
	return cm, err
}

func (r *NodePowerAgentReconciler) ensureNodePowerAgentSecret(ctx context.Context, agent *powerv1alpha1.NodePowerAgent, namespace string, data map[string][]byte) (*corev1.Secret, error) {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: nodePowerAgentSecretName(agent), Namespace: namespace}}
	_, err := controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		secret.Labels = labelsForNodePowerAgent(agent)
		secret.Type = corev1.SecretTypeOpaque
		secret.Data = data
		return controllerutil.SetControllerReference(agent, secret, r.Scheme)
	})
	return secret, err
}
