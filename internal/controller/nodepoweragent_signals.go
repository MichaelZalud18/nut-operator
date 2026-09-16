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
	"encoding/json"
	"fmt"
	"time"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/nodeagent"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

func (r *NodePowerAgentReconciler) ensureNodePowerAgentSignalSecret(ctx context.Context, agent *powerv1alpha1.NodePowerAgent, namespace string) (*corev1.Secret, error) {
	revoked, err := r.revokedSignalKeys(ctx, agent, namespace)
	if err != nil {
		return nil, err
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: nodePowerAgentSignalSecretName(agent), Namespace: namespace}}
	_, err = controllerutil.CreateOrUpdate(ctx, r.Client, secret, func() error {
		secret.Labels = labelsForNodePowerAgent(agent)
		secret.Type = corev1.SecretTypeOpaque
		if secret.Data == nil {
			secret.Data = map[string][]byte{}
		}
		// Withdraw the signals whose authorization has ended (F-87). Absence is the record that the
		// episode is over: the operator writes a node's signal and, until this, never took it back, so
		// the file outlived the halt it authorized. Every actuator pod starts with an empty seen-set --
		// F-58's dedupe lives on a per-pod emptyDir -- so any pod replacement inside the TTL (rollout,
		// kubelet restart, OOM, eviction) read a signal that was already spent and halted the node
		// again. The worst shape is power restoration: nodes boot inside the TTL of the very signal
		// that took them down and immediately take themselves back down.
		//
		// This is the primary guard. The TTL is a backstop for the case where the operator is not
		// around to revoke, and F-58's dedupe stays as defense in depth for the pod that is.
		for key := range revoked {
			delete(secret.Data, key)
		}
		// The marker the actuator's readiness looks for (F-86). An empty Secret projects as an empty
		// directory, which is exactly what kubelet mounts when the Secret is missing entirely, so
		// without a key that is always present there is nothing to tell "no flow running" apart from
		// "this channel does not exist". Revocation makes an empty Secret the steady state rather than
		// an edge case, so the marker matters more here than it did when it was written.
		//
		// The value is the agent's generation rather than a timestamp: it has to be stable across
		// reconciles, or every pass would rewrite the Secret and re-trigger kubelet projection on
		// every node for no reason.
		secret.Data[nodeagent.DeliveryChannelMarker] = []byte(fmt.Sprintf("%s/%s@%d", agent.Namespace, agent.Name, agent.Generation))
		return controllerutil.SetControllerReference(agent, secret, r.Scheme)
	})
	return secret, err
}

// revokedSignalKeys names the per-node signals in the projected Secret that may no longer sit there.
//
// Read before the CreateOrUpdate rather than inside its mutate function so the ShutdownFlow lookups
// happen once per pass instead of once per conflict retry. A key that appears between this read and
// the update is simply not considered this time around, which is the safe direction: the next
// reconcile revokes it if it is spent, and nothing is withdrawn on the strength of a stale read.
func (r *NodePowerAgentReconciler) revokedSignalKeys(ctx context.Context, agent *powerv1alpha1.NodePowerAgent, namespace string) (map[string]struct{}, error) {
	key := types.NamespacedName{Namespace: namespace, Name: nodePowerAgentSignalSecretName(agent)}
	var secret corev1.Secret
	if err := r.Get(ctx, key, &secret); err != nil {
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("get signal Secret %s/%s for revocation: %w", key.Namespace, key.Name, err)
	}

	ttl := durationOrDefault(agent.Spec.Shutdown.SignalTTL, 2*time.Minute)
	now := time.Now().UTC()
	var revoked map[string]struct{}
	flows := map[string]*powerv1alpha1.ShutdownFlow{}
	for name, raw := range secret.Data {
		if name == nodeagent.DeliveryChannelMarker {
			continue
		}
		var payload nodeagent.ShutdownSignal
		if err := json.Unmarshal(raw, &payload); err != nil {
			// Unparseable. signalStillAuthorized rejects the zero value on the same grounds
			// InspectSignal would, so this falls through to revocation rather than lingering.
			payload = nodeagent.ShutdownSignal{}
		}
		flow, err := r.shutdownFlowForSignal(ctx, payload.ShutdownFlow, flows)
		if err != nil {
			return nil, err
		}
		if signalStillAuthorized(payload, flow, ttl, now) {
			continue
		}
		if revoked == nil {
			revoked = map[string]struct{}{}
		}
		revoked[name] = struct{}{}
	}
	return revoked, nil
}

// shutdownFlowForSignal resolves the flow a signal names, caching per pass. A nil flow and a nil
// error means the flow does not exist, which is an answer and not a failure.
func (r *NodePowerAgentReconciler) shutdownFlowForSignal(ctx context.Context, name string, cache map[string]*powerv1alpha1.ShutdownFlow) (*powerv1alpha1.ShutdownFlow, error) {
	if name == "" {
		return nil, nil
	}
	if flow, resolved := cache[name]; resolved {
		return flow, nil
	}
	var flow powerv1alpha1.ShutdownFlow
	if err := r.Get(ctx, types.NamespacedName{Name: name}, &flow); err != nil {
		if apierrors.IsNotFound(err) {
			cache[name] = nil
			return nil, nil
		}
		return nil, fmt.Errorf("get ShutdownFlow %q for signal revocation: %w", name, err)
	}
	cache[name] = &flow
	return &flow, nil
}

// signalStillAuthorized reports whether a signal already sitting in the projected Secret may stay.
//
// The flow's LastExecution is the operator's record of which halt episode is current, so it is what
// decides whether a signal is still speaking for a live one. Two things have to hold, and the order
// matters:
//
// The signal must not be newer than the execution record. recordShutdownFlowExecution runs the whole
// executor synchronously and only then writes LastExecution, so between the runner putting a signal
// in the Secret and the status landing there is a window where the freshest signal in the cluster
// belongs to an execution the flow has not admitted to yet. Comparing timestamps closes that window
// exactly, with no grace constant to tune: a signal written after the last recorded execution
// finished can only belong to one still in flight, and revoking it would cancel a shutdown mid-flow.
//
// Then the episode must still be live -- same execution, trigger still active. TriggerActive is the
// flow's own answer to "is the power event that authorized this still happening", cleared by
// deactivateLastExecution the moment the trigger stops being eligible. A signal from a superseded
// execution, or from this one after the trigger cleared, has nothing left to authorize.
//
// Keeping a signal while its episode is live is deliberate: a pod that restarts before its node has
// actually halted should read the signal and finish the job.
//
// Revocation therefore needs positive evidence that an episode ended. Where there is none -- the
// flow cannot be found at all -- the signal is kept until its TTL runs out and then cleaned up,
// because at that point the actuator rejects it anyway and removing it is bookkeeping rather than a
// withdrawal of authority. Revoking a missing-flow signal on sight was the first shape here and it
// was wrong twice over: a flow the cache has not synced yet is indistinguishable from one that was
// deleted, so a cold cache could cancel a live shutdown, and "I cannot find the flow" is not
// evidence that the halt it authorized already happened.
func signalStillAuthorized(payload nodeagent.ShutdownSignal, flow *powerv1alpha1.ShutdownFlow, ttl time.Duration, now time.Time) bool {
	written, err := time.Parse(time.RFC3339Nano, payload.Timestamp)
	if err != nil || payload.ExecutionID == "" || payload.NodeName == "" || payload.ShutdownFlow == "" {
		// Nothing an actuator would act on: InspectSignal rejects it on the same grounds. There is
		// no live episode to protect, so it goes.
		return false
	}
	if flow == nil {
		return now.Sub(written) <= ttl
	}
	last := flow.Status.LastExecution
	if last == nil {
		// No execution evidence to compare against. A signal exists, so an execution is either in
		// flight or its record was lost; neither is grounds to withdraw a halt.
		return true
	}
	if last.CompletedAt == nil || written.After(last.CompletedAt.Time) {
		return true
	}
	return last.ExecutionID == payload.ExecutionID && last.TriggerActive
}

// nodePowerAgentDeclaredShutdownFlow is the flow the agent actually declares, or empty.
//
// Distinct from nodePowerAgentShutdownFlowName, which substitutes "upsmon-local" when no reference
// is set. That substitution is right for the local writer -- it needs some name to stamp -- and
// wrong for the actuator, which compares what it is given against what arrives. An agent that
// declares no flow accepts a release from any flow, which is the model the operator already has:
// a ShutdownFlow names its agents through AgentRefs, and the agent's reference back is optional.
func nodePowerAgentDeclaredShutdownFlow(agent *powerv1alpha1.NodePowerAgent) string {
	if agent.Spec.ShutdownFlowRef != nil {
		return agent.Spec.ShutdownFlowRef.Name
	}
	return ""
}

func nodePowerAgentShutdownFlowName(agent *powerv1alpha1.NodePowerAgent) string {
	if agent.Spec.ShutdownFlowRef != nil && agent.Spec.ShutdownFlowRef.Name != "" {
		return agent.Spec.ShutdownFlowRef.Name
	}
	return "upsmon-local"
}

func nodePowerAgentSignalKey(nodeName string) string {
	return nodeName + ".json"
}
