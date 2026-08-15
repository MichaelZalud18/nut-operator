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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/selection"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"
	"sigs.k8s.io/controller-runtime/pkg/source"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

// nodePowerAgentFinalizer mirrors nutServerFinalizer's reasoning (F-1): owner-reference garbage
// collection already reliably deletes the rendered DaemonSet/ConfigMap/Secret/ServiceAccount/
// NetworkPolicy; this makes deletion an explicit, blocking step with a Kubernetes Event instead of a
// silent one.
const nodePowerAgentFinalizer = "power.zalud.io/nodepoweragent-cleanup"

// nodePowerAgentPodLabelKey is set on every Pod rendered for a NodePowerAgent (labelsForNodePowerAgent
// in nodepoweragent_render.go) and is also the label the dedicated Pod cache in SetupWithManager
// filters on (F-32) -- shared as one constant so the two can never drift apart.
const nodePowerAgentPodLabelKey = "power.zalud.io/nodepoweragent"

// NodePowerAgentReconciler reconciles a NodePowerAgent object
type NodePowerAgentReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder events.EventRecorder
}

// +kubebuilder:rbac:groups=power.zalud.io,resources=nodepoweragents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=power.zalud.io,resources=nodepoweragents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=power.zalud.io,resources=nodepoweragents/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=power.zalud.io,resources=nutservers;upsdevices;powermanagementclusters;powerinventorynodes;shutdownflows,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=namespaces,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=configmaps;secrets;serviceaccounts,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups=apps,resources=daemonsets,verbs=get;list;watch;create;update;patch
// +kubebuilder:rbac:groups=networking.k8s.io,resources=networkpolicies,verbs=get;list;watch;create;update;patch

// Reconcile validates node agent safety settings and records DaemonSet rendering status.
func (r *NodePowerAgentReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var agent powerv1alpha1.NodePowerAgent
	if err := r.Get(ctx, req.NamespacedName, &agent); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	base := agent.DeepCopy()

	if !agent.DeletionTimestamp.IsZero() {
		return r.finalizeNodePowerAgent(ctx, &agent)
	}
	if !controllerutil.ContainsFinalizer(&agent, nodePowerAgentFinalizer) {
		controllerutil.AddFinalizer(&agent, nodePowerAgentFinalizer)
		if err := r.Update(ctx, &agent); err != nil {
			return ctrl.Result{}, err
		}
	}

	result := validateNodePowerAgent(&agent)
	reconcileResult := ctrl.Result{}
	agent.Status.ObservedGeneration = agent.Generation
	if result.accepted {
		rendered, renderErr := r.reconcileNodePowerAgentOperands(ctx, &agent)
		if renderErr != nil {
			reconcileResult = ctrl.Result{RequeueAfter: time.Minute}
			agent.Status.Phase = powerv1alpha1.NodePowerAgentPhaseError
			agent.Status.SelectedNodes = nil
			agent.Status.DesiredNumberScheduled = 0
			agent.Status.NumberReady = 0
			agent.Status.ReadyNodeCount = 0
			agent.Status.UnavailableNodeCount = 0
			agent.Status.NodeStatuses = nil
			agent.Status.ConfigHash = ""
			agent.Status.ManagedResources = nil
			setAcceptedCondition(&agent.Status.Conditions, agent.Generation, result)
			setReadyCondition(
				&agent.Status.Conditions,
				agent.Generation,
				false,
				"OperandRenderingFailed",
				renderErr.Error(),
			)
			setDegradedCondition(&agent.Status.Conditions, agent.Generation, true, "OperandRenderingFailed", renderErr.Error())
		} else {
			agent.Status.Phase = powerv1alpha1.NodePowerAgentPhaseRendering
			agent.Status.SelectedNodes = rendered.SelectedNodes
			agent.Status.DesiredNumberScheduled = rendered.DesiredNumberScheduled
			agent.Status.NumberReady = rendered.NumberReady
			agent.Status.ReadyNodeCount = rendered.ReadyNodeCount
			agent.Status.UnavailableNodeCount = rendered.UnavailableNodeCount
			agent.Status.NodeStatuses = rendered.NodeStatuses
			agent.Status.ConfigHash = rendered.ConfigHash
			agent.Status.ManagedResources = rendered.ManagedResources
			agent.Status.UncoveredNodes = rendered.UncoveredNodes
			setAcceptedCondition(&agent.Status.Conditions, agent.Generation, result)
			ready := rendered.NumberReady >= rendered.DesiredNumberScheduled && rendered.UnavailableNodeCount == 0
			reason := "AwaitingDaemonSet"
			message := "Node power agent operands rendered; waiting for DaemonSet readiness"
			if ready {
				agent.Status.Phase = powerv1alpha1.NodePowerAgentPhaseReady
				reason = "Ready"
				message = "Node power agent operands are ready"
			} else if rendered.UnavailableNodeCount > 0 {
				agent.Status.Phase = powerv1alpha1.NodePowerAgentPhaseDegraded
				reason = "AgentPodsUnavailable"
				message = "Node power agent operands rendered, but one or more selected nodes lack a ready agent pod"
			}
			setReadyCondition(&agent.Status.Conditions, agent.Generation, ready, reason, message)

			// A TLS downgrade does not block readiness — upsmon still monitors, which is the whole
			// point of the agent — but it is a divergence from declared intent and has to show up
			// somewhere. Pod unavailability outranks it as a Degraded reason because it means the
			// node has no monitoring at all.
			degradedReason, degradedMessage := reason, message
			if rendered.UnavailableNodeCount == 0 && rendered.TLSDowngradeReason != "" {
				degradedReason = "NUTTLSDowngraded"
				degradedMessage = "upsmon.conf is less strict than the monitored NUTServers request: " + rendered.TLSDowngradeReason
			}
			// A Pod Security conflict outranks both (F-62). It is not a competing symptom, it is the
			// cause of the unavailability directly above: the pods are not late, they were refused
			// admission, and the only place that shows is a kubelet event on a pod that does not
			// exist. Naming the exception here is the difference between a five-minute fix and an
			// afternoon.
			// F-74: a node the inventory describes and this agent does not select is not
			// unavailable and not degraded by any other measure -- it is simply absent, and the
			// agent reports ready over a fleet it partly covers. Ranked below admission rejection,
			// which stops the agent existing at all, and above a TLS downgrade, which still
			// monitors.
			if rendered.PodSecurityConflict == "" && len(rendered.UncoveredNodes) > 0 {
				degradedReason = "InventoryNodesUncovered"
				degradedMessage = fmt.Sprintf("spec.nodeSelector does not match %d node(s) the power inventory describes: %s",
					len(rendered.UncoveredNodes), strings.Join(rendered.UncoveredNodes, ", "))
			}
			if rendered.PodSecurityConflict != "" {
				degradedReason = "PodSecurityRejectsActuation"
				degradedMessage = rendered.PodSecurityConflict
			}
			degraded := rendered.UnavailableNodeCount > 0 ||
				rendered.TLSDowngradeReason != "" ||
				rendered.PodSecurityConflict != "" ||
				len(rendered.UncoveredNodes) > 0
			setDegradedCondition(&agent.Status.Conditions, agent.Generation, degraded, degradedReason, degradedMessage)
		}
	} else {
		agent.Status.Phase = powerv1alpha1.NodePowerAgentPhaseError
		agent.Status.SelectedNodes = nil
		agent.Status.DesiredNumberScheduled = 0
		agent.Status.NumberReady = 0
		agent.Status.ReadyNodeCount = 0
		agent.Status.UnavailableNodeCount = 0
		agent.Status.NodeStatuses = nil
		agent.Status.ConfigHash = ""
		agent.Status.ManagedResources = nil
		setAcceptedCondition(&agent.Status.Conditions, agent.Generation, result)
		setReadyCondition(
			&agent.Status.Conditions,
			agent.Generation,
			false,
			"ValidationFailed",
			result.message,
		)
		setDegradedCondition(&agent.Status.Conditions, agent.Generation, true, result.reason, result.message)
	}

	if err := r.Status().Patch(ctx, &agent, client.MergeFrom(base)); err != nil {
		log.Error(err, "failed to update NodePowerAgent status")
		return ctrl.Result{}, err
	}

	return reconcileResult, nil
}

// finalizeNodePowerAgent runs when a NodePowerAgent is being deleted. See finalizeNUTServer's comment
// for the same reasoning: owner-reference garbage collection already handles the rendered child
// resources correctly; this adds an observable Event and a blocking deletion step where there was
// previously none (F-1).
func (r *NodePowerAgentReconciler) finalizeNodePowerAgent(ctx context.Context, agent *powerv1alpha1.NodePowerAgent) (ctrl.Result, error) {
	if !controllerutil.ContainsFinalizer(agent, nodePowerAgentFinalizer) {
		return ctrl.Result{}, nil
	}
	if r.Recorder != nil {
		r.Recorder.Eventf(agent, nil, corev1.EventTypeNormal, "OperandTeardown", "Delete",
			"NodePowerAgent deleted; rendered operands are being garbage-collected via owner references")
	}
	controllerutil.RemoveFinalizer(agent, nodePowerAgentFinalizer)
	if err := r.Update(ctx, agent); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
//
// The Pod watch is deliberately not a plain Watches(&corev1.Pod{}, ...) (F-32): that would make the
// manager's shared cache -- the same cache that backs mgr.GetClient(), which internal/kubeactions.Runner
// uses for its own unrestricted, cluster-wide Pod list during DrainNodes eviction -- watch and hold
// every Pod in the cluster in memory, only to discard almost all of them in the map function below.
// Instead this builds a second, dedicated cache scoped server-side (via a label selector on the
// LIST/WATCH request itself, not just client-side filtering) to Pods carrying
// nodePowerAgentPodLabelKey, and wires it in as a raw source. That cache is entirely independent of
// mgr.GetCache()/mgr.GetClient(), so it cannot affect any other controller's or Runner's Pod reads.
func (r *NodePowerAgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	ownedPodsRequirement, err := labels.NewRequirement(nodePowerAgentPodLabelKey, selection.Exists, nil)
	if err != nil {
		return fmt.Errorf("build NodePowerAgent Pod cache label selector: %w", err)
	}
	podCache, err := cache.New(mgr.GetConfig(), cache.Options{
		Scheme:     mgr.GetScheme(),
		Mapper:     mgr.GetRESTMapper(),
		HTTPClient: mgr.GetHTTPClient(),
		ByObject: map[client.Object]cache.ByObject{
			&corev1.Pod{}: {Label: labels.NewSelector().Add(*ownedPodsRequirement)},
		},
	})
	if err != nil {
		return fmt.Errorf("build NodePowerAgent Pod cache: %w", err)
	}
	if err := mgr.Add(podCache); err != nil {
		return fmt.Errorf("register NodePowerAgent Pod cache with manager: %w", err)
	}

	podSource := source.Kind(podCache, &corev1.Pod{}, handler.TypedEnqueueRequestsFromMapFunc(
		func(_ context.Context, pod *corev1.Pod) []reconcile.Request {
			agentName := pod.GetLabels()[nodePowerAgentPodLabelKey]
			if agentName == "" {
				return nil
			}
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: agentName}}}
		},
	))

	return ctrl.NewControllerManagedBy(mgr).
		For(&powerv1alpha1.NodePowerAgent{}).
		Owns(&appsv1.DaemonSet{}).
		Owns(&corev1.ConfigMap{}).
		WatchesRawSource(podSource).
		// F-74's coverage check is computed against the inventory, so it has to be recomputed when
		// the inventory changes and not only when the agent does. Without this the check answered
		// correctly and answered late: adding a PowerInventoryNode the selector misses left every
		// agent reporting Ready until something unrelated happened to trigger a reconcile.
		Watches(&powerv1alpha1.PowerInventoryNode{}, handler.EnqueueRequestsFromMapFunc(r.enqueueAllNodePowerAgents)).
		// Signal revocation (F-87) is decided against the flow's LastExecution, so the instant that
		// record stops covering a signal is the instant the signal becomes withdrawable. Without this
		// watch the withdrawal waits for whatever unrelated event next reconciles the agent, and the
		// wait it would lose is exactly the one that matters: a node rebooting after power returns,
		// racing the TTL of the signal that took it down.
		Watches(&powerv1alpha1.ShutdownFlow{}, handler.EnqueueRequestsFromMapFunc(r.enqueueAllNodePowerAgents)).
		Owns(&corev1.Secret{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Named("nodepoweragent").
		Complete(r)
}

// enqueueAllNodePowerAgents re-evaluates every agent when the inventory or a ShutdownFlow changes.
//
// Coverage is a property of the inventory and the agent's selector together (F-74), so either side
// changing can turn a covered fleet into a partly covered one. Signal revocation (F-87) is likewise
// a property of a flow's execution record and whichever agents' Secrets carry signals from it. In
// both cases there is no cheap way to know which agents a given object affects -- the answer depends
// on each agent's selector, or on Secret contents -- and the number of NodePowerAgents in a cluster
// is small, so all of them are re-evaluated.
func (r *NodePowerAgentReconciler) enqueueAllNodePowerAgents(ctx context.Context, _ client.Object) []reconcile.Request {
	var agents powerv1alpha1.NodePowerAgentList
	if err := r.List(ctx, &agents); err != nil {
		logf.FromContext(ctx).Error(err, "failed to list NodePowerAgent resources for inventory coverage re-evaluation")
		return nil
	}
	requests := make([]reconcile.Request, 0, len(agents.Items))
	for i := range agents.Items {
		requests = append(requests, reconcile.Request{
			NamespacedName: types.NamespacedName{Name: agents.Items[i].Name},
		})
	}
	return requests
}
