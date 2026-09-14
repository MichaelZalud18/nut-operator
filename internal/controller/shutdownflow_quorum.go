package controller

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/MichaelZalud18/nut-operator/internal/executor"
	"github.com/MichaelZalud18/nut-operator/internal/nodeagent"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func (r *ShutdownFlowReconciler) validateControlPlaneRelease(ctx context.Context, release executor.NodeRelease) error {
	members := map[string]bool{}
	for _, node := range release.ControlPlaneNodes {
		members[node] = false
	}
	for _, node := range release.QuorumMembers {
		members[node] = true
	}
	if _, ok := members[release.NodeName]; !ok {
		return nil
	}
	if release.TerminalHandoff {
		return nil
	}
	var nodes corev1.NodeList
	if err := r.reader().List(ctx, &nodes); err != nil {
		return fmt.Errorf("read control-plane readiness: %w", err)
	}
	var secrets corev1.SecretList
	if err := r.reader().List(ctx, &secrets, client.MatchingLabels{"app.kubernetes.io/managed-by": "nut-operator"}); err != nil {
		return fmt.Errorf("read pending control-plane releases: %w", err)
	}
	pending := map[string]bool{release.NodeName: true}
	for _, secret := range secrets.Items {
		if _, channel := secret.Data[nodeagent.DeliveryChannelMarker]; !channel && secret.Labels["power.zalud.io/nodepoweragent"] == "" {
			continue
		}
		for key, data := range secret.Data {
			if key == "delivery-channel" {
				continue
			}
			var signal nodeagent.ShutdownSignal
			if err := json.Unmarshal(data, &signal); err != nil {
				return fmt.Errorf("invalid pending shutdown signal in Secret %s/%s", secret.Namespace, secret.Name)
			}
			pending[signal.NodeName] = true
		}
	}
	totalVoters, readyVoters, readyControlPlane := 0, 0, 0
	for _, voter := range members {
		if voter {
			totalVoters++
		}
	}
	if len(members) > 1 && totalVoters == 0 {
		return fmt.Errorf("control-plane quorum membership is undeclared; release requires terminal handoff")
	}
	for _, node := range nodes.Items {
		voter, known := members[node.Name]
		if !known || pending[node.Name] || node.DeletionTimestamp != nil {
			continue
		}
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				readyControlPlane++
				if voter {
					readyVoters++
				}
				break
			}
		}
	}
	if readyControlPlane == 0 || (totalVoters > 0 && readyVoters < totalVoters/2+1) {
		return fmt.Errorf("release of %q would leave %d ready control-plane nodes and %d of %d quorum members before terminal handoff", release.NodeName, readyControlPlane, readyVoters, totalVoters)
	}
	return nil
}
