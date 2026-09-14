package kubeactions

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/executor"
	"github.com/MichaelZalud18/nut-operator/internal/nodeagent"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func terminalControlPlaneHandoff(action executor.Action) bool {
	for _, release := range action.Group.NodeReleases {
		if release.TerminalHandoff && (slices.Contains(release.ControlPlaneNodes, release.NodeName) || slices.Contains(release.QuorumMembers, release.NodeName)) {
			return true
		}
	}
	return false
}

func (r Runner) terminalControlPlaneHandoff(ctx context.Context, action executor.Action) (executor.ActionOutcome, error) {
	if err := signalPublicationGate.Acquire(ctx, 1); err != nil {
		return blocked(err), err
	}
	defer signalPublicationGate.Release(1)
	if r.ValidateNodeRelease == nil {
		err := fmt.Errorf("terminal handoff requires a live node release validator")
		return blocked(err), err
	}
	first := action.Group.NodeReleases[0]
	key := client.ObjectKey{Namespace: first.SignalSecretNamespace, Name: first.SignalSecretName}
	keys := map[string]bool{}
	for _, release := range action.Group.NodeReleases {
		if !release.TerminalHandoff || release.NodeName == "" || key.Namespace == "" || key.Name == "" || release.SignalSecretKey == "" || release.SignalSecretNamespace != key.Namespace || release.SignalSecretName != key.Name || release.NodePowerAgent != first.NodePowerAgent || keys[release.SignalSecretKey] {
			err := fmt.Errorf("terminal control-plane handoff requires distinct node keys in one agent signal Secret")
			return blocked(err), err
		}
		keys[release.SignalSecretKey] = true
	}
	var secret corev1.Secret
	err := r.Client.Get(ctx, key, &secret)
	create := apierrors.IsNotFound(err)
	if err != nil && !create {
		return blocked(err), err
	}
	if create {
		secret = corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: key.Namespace, Name: key.Name}, Type: corev1.SecretTypeOpaque}
	}
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	if secret.Labels == nil {
		secret.Labels = map[string]string{}
	}
	for label, value := range signalSecretLabels(action, first) {
		secret.Labels[label] = value
	}
	issued := r.now()
	var results []executor.NodeSignalResult
	for _, release := range action.Group.NodeReleases {
		payload, err := json.Marshal(nodeagent.ShutdownSignal{ExecutionID: action.ExecutionID, NodeName: release.NodeName, PlanConfigHash: action.PlanConfigHash, Reason: "ReleaseApproved", SelectedUPSDevices: append([]string(nil), action.SelectedUPSDevices...), ShutdownFlow: action.ShutdownFlow, Timestamp: issued.UTC().Format(time.RFC3339Nano), SkipSync: action.TierOverrunning})
		if err != nil {
			return blocked(err), err
		}
		secret.Data[release.SignalSecretKey] = append(payload, '\n')
		results = append(results, executor.NodeSignalResult{NodeName: release.NodeName, NodePowerAgent: release.NodePowerAgent, SignalSecretNamespace: key.Namespace, SignalSecretName: key.Name, SignalSecretKey: release.SignalSecretKey, IssuedAt: issued, SkipSync: action.TierOverrunning})
	}
	for _, release := range action.Group.NodeReleases {
		if err := r.ValidateNodeRelease(ctx, release); err != nil {
			return blocked(err), err
		}
	}
	if create {
		err = r.Client.Create(ctx, &secret)
	} else {
		err = r.Client.Update(ctx, &secret)
	}
	if err != nil {
		out := blocked(err)
		out.SignalResults = results
		return out, err
	}
	for i := range results {
		results[i].Published = true
		if r.SignalWritten != nil {
			r.SignalWritten(results[i].NodeName, action.ShutdownFlow, action.ExecutionID, issued)
		}
	}
	return executor.ActionOutcome{Outcome: executor.OutcomeSucceeded, SignalResults: results, Details: map[string]any{"handoff": "ProjectedSecretSignal", "terminalBatch": true, "nodeReleases": len(results), "signalSecrets": 1, "syncSkipped": action.TierOverrunning}}, nil
}
