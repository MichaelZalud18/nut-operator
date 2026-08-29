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

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/haltwatch"
	"github.com/MichaelZalud18/nut-operator/internal/metrics"
	"github.com/MichaelZalud18/nut-operator/internal/nodeagent"
)

// nodeHaltSweepInterval is how often unresolved halt attempts are checked against their deadline.
//
// The sweep exists for the case where nothing else will ever wake this controller: a node that was
// signalled and simply kept running produces no further events, so without a timer the attempt stays
// open forever and the failure is never counted. A node that does halt is resolved by its own watch
// event, not by this.
const nodeHaltSweepInterval = time.Minute

// NodeHaltReconciler records whether nodes asked to power off actually stopped.
//
// It reconciles core Nodes, which no other reconciler in this operator owns, and it writes no
// objects at all -- its only output is metrics. That is the point: the evidence has to be produced
// by something that is still running after the node is not, and it must not need anything from the
// node to do it.
type NodeHaltReconciler struct {
	client.Client
	Observer *haltwatch.Observer
	// Clock is overridable for tests.
	Clock func() time.Time
}

// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch
// +kubebuilder:rbac:groups=power.zalud.io,resources=nodepoweragents;powermanagementclusters;shutdownflows,verbs=get;list;watch

// Reconcile resolves a pending halt attempt when its node stops reporting Ready.
func (r *NodeHaltReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var node corev1.Node
	if err := r.Get(ctx, req.NamespacedName, &node); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		// The Node object is gone, which is not what powering off looks like -- a halted node keeps
		// its Node object and sits at NotReady. Someone removed this machine from the cluster, so
		// its series are dropped rather than left to accumulate across rebuilds.
		r.Observer.Forget(req.Name)
		metrics.HaltLastVerifiedTimestampSeconds.DeleteLabelValues(req.Name)
		metrics.HaltDurationSeconds.DeleteLabelValues(req.Name)
		return ctrl.Result{}, nil
	}

	if haltwatch.Reporting(&node) {
		return ctrl.Result{}, nil
	}

	observedAt := haltwatch.LastAliveAt(&node)
	if observedAt.IsZero() {
		observedAt = r.now()
	}
	result, resolved := r.Observer.NodeStopped(node.Name, observedAt)
	if !resolved {
		// Nodes go NotReady for reasons that have nothing to do with this operator. Only a node with
		// a signal outstanding is a halt attempt.
		return ctrl.Result{}, nil
	}

	log.Info("Observed a signalled node stop reporting",
		"node", result.Node, "shutdownflow", result.ShutdownFlow, "executionID", result.ExecutionID,
		"duration", result.Duration.Round(time.Millisecond))
	publishHaltResult(result)
	return ctrl.Result{}, nil
}

// Start runs the deadline sweep until the context is cancelled. It implements manager.Runnable.
func (r *NodeHaltReconciler) Start(ctx context.Context) error {
	log := logf.FromContext(ctx).WithName("nodehalt-sweep")
	if seeded, err := r.seedPendingAttempts(ctx); err != nil {
		log.Error(err, "Could not seed pending halt attempts from signal Secrets")
	} else if seeded > 0 {
		log.Info("Seeded pending halt attempts from signal Secrets", "attempts", seeded)
	}
	ticker := time.NewTicker(nodeHaltSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			for _, result := range r.Observer.Expire(r.now()) {
				// A node that was told to stop and did not is the finding this whole component
				// exists to surface, so it is logged at Info with the flow that asked.
				log.Info("Signalled node was still reporting Ready at the deadline",
					"node", result.Node, "shutdownflow", result.ShutdownFlow,
					"executionID", result.ExecutionID, "elapsed", result.Duration.Round(time.Second))
				publishHaltResult(result)
			}
		}
	}
}

// seedPendingAttempts reconstructs in-memory halt attempts from the operator's projected signal
// Secrets after a manager restart or leader handoff.
//
// Only nodes still reporting Ready are retained. A live signal plus an already-NotReady Node is an
// evidence-model question the signal alone cannot settle: it might be the requested halt, a
// partition, or a node that was already unhealthy. Keeping it pending would later publish a timeout
// against a node that may have halted before this process started, while resolving it would claim
// evidence the restarted process did not observe. So this seed closes the restart window where the
// node is still up, and leaves the already-gone case to a separate decision.
func (r *NodeHaltReconciler) seedPendingAttempts(ctx context.Context) (int, error) {
	if r.Observer == nil {
		return 0, nil
	}
	var agents powerv1alpha1.NodePowerAgentList
	if err := r.List(ctx, &agents); err != nil {
		return 0, fmt.Errorf("list NodePowerAgents for halt attempt seed: %w", err)
	}

	now := r.now()
	flows := map[string]*powerv1alpha1.ShutdownFlow{}
	seeded := 0
	for i := range agents.Items {
		agent := &agents.Items[i]
		namespace, err := r.nodePowerAgentSignalNamespace(ctx, agent)
		if err != nil {
			return seeded, err
		}
		secret := &corev1.Secret{}
		key := types.NamespacedName{Namespace: namespace, Name: nodePowerAgentSignalSecretName(agent)}
		if err := r.Get(ctx, key, secret); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return seeded, fmt.Errorf("get signal Secret %s/%s for halt attempt seed: %w", key.Namespace, key.Name, err)
		}

		ttl := durationOrDefault(agent.Spec.Shutdown.SignalTTL, 2*time.Minute)
		for name, raw := range secret.Data {
			if name == nodeagent.DeliveryChannelMarker {
				continue
			}
			var payload nodeagent.ShutdownSignal
			if err := json.Unmarshal(raw, &payload); err != nil {
				continue
			}
			flow, err := r.shutdownFlowForSignal(ctx, payload.ShutdownFlow, flows)
			if err != nil {
				return seeded, err
			}
			if !signalStillAuthorized(payload, flow, ttl, now) {
				continue
			}
			written, err := time.Parse(time.RFC3339Nano, payload.Timestamp)
			if err != nil {
				continue
			}
			attempt := haltwatch.Attempt{
				Node:            payload.NodeName,
				ShutdownFlow:    payload.ShutdownFlow,
				ExecutionID:     payload.ExecutionID,
				SignalWrittenAt: written.UTC(),
			}
			r.Observer.SignalWritten(attempt)

			reporting, err := r.nodeReportsReady(ctx, payload.NodeName)
			if err != nil {
				r.Observer.Forget(payload.NodeName)
				return seeded, err
			}
			if !reporting {
				r.Observer.Forget(payload.NodeName)
				continue
			}
			seeded++
		}
	}
	return seeded, nil
}

func (r *NodeHaltReconciler) nodePowerAgentSignalNamespace(ctx context.Context, agent *powerv1alpha1.NodePowerAgent) (string, error) {
	if agent.Spec.Namespace != "" || agent.Spec.ManagementClusterRef == nil || agent.Spec.ManagementClusterRef.Name == "" {
		return nodePowerAgentNamespace(agent, nil), nil
	}
	cluster := &powerv1alpha1.PowerManagementCluster{}
	name := agent.Spec.ManagementClusterRef.Name
	if err := r.Get(ctx, types.NamespacedName{Name: name}, cluster); err != nil {
		return "", fmt.Errorf("get PowerManagementCluster %q for halt attempt seed: %w", name, err)
	}
	return nodePowerAgentNamespace(agent, cluster), nil
}

func (r *NodeHaltReconciler) shutdownFlowForSignal(ctx context.Context, name string, cache map[string]*powerv1alpha1.ShutdownFlow) (*powerv1alpha1.ShutdownFlow, error) {
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
		return nil, fmt.Errorf("get ShutdownFlow %q for halt attempt seed: %w", name, err)
	}
	cache[name] = &flow
	return &flow, nil
}

func (r *NodeHaltReconciler) nodeReportsReady(ctx context.Context, name string) (bool, error) {
	if name == "" {
		return false, nil
	}
	var node corev1.Node
	if err := r.Get(ctx, types.NamespacedName{Name: name}, &node); err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("get Node %q for halt attempt seed: %w", name, err)
	}
	return haltwatch.Reporting(&node), nil
}

// NeedLeaderElection reports true. Unlike the certificate reporter, this is cluster state rather
// than per-replica state, and a second replica would double-count every attempt.
func (r *NodeHaltReconciler) NeedLeaderElection() bool { return true }

func publishHaltResult(result haltwatch.Result) {
	metrics.HaltAttemptsTotal.WithLabelValues(result.ShutdownFlow, string(result.Outcome)).Inc()
	if result.Outcome != haltwatch.OutcomeHalted {
		// Only a halt that was actually observed updates the per-node evidence. Overwriting the
		// last-verified timestamp on a failed attempt would turn the one metric that answers "has
		// this node ever been proven to halt" into "when was this node last asked to".
		return
	}
	metrics.HaltLastVerifiedTimestampSeconds.WithLabelValues(result.Node).Set(float64(result.ObservedAt.Unix()))
	metrics.HaltDurationSeconds.WithLabelValues(result.Node).Set(result.Duration.Seconds())
}

func (r *NodeHaltReconciler) now() time.Time {
	if r.Clock != nil {
		return r.Clock().UTC()
	}
	return time.Now().UTC()
}

// SetupWithManager wires the Node watch.
//
// The predicate is what keeps a cluster-wide Node watch cheap. Node objects are updated by kubelet
// and by anything that labels or taints them, and none of that can resolve a halt: only a node with
// an outstanding signal is a candidate. With no shutdown in flight the filter drops every event
// before it becomes a reconcile, so the steady-state cost is the informer that already exists for
// the other reconcilers' node reads.
//
// Deletes are always admitted, because that path exists to drop metric series and there is nothing
// pending to test against by then.
func (r *NodeHaltReconciler) SetupWithManager(mgr ctrl.Manager) error {
	watching := predicate.Funcs{
		CreateFunc:  func(e event.CreateEvent) bool { return r.Observer.Watching(e.Object.GetName()) },
		UpdateFunc:  func(e event.UpdateEvent) bool { return r.Observer.Watching(e.ObjectNew.GetName()) },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(e event.GenericEvent) bool { return r.Observer.Watching(e.Object.GetName()) },
	}
	if err := mgr.Add(r); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}, builder.WithPredicates(watching)).
		Named("nodehalt").
		Complete(r)
}
