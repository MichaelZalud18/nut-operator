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
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

// NodePowerAgentReconciler reconciles a NodePowerAgent object
type NodePowerAgentReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=power.zalud.io,resources=nodepoweragents,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=power.zalud.io,resources=nodepoweragents/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=power.zalud.io,resources=nodepoweragents/finalizers,verbs=update
// +kubebuilder:rbac:groups=power.zalud.io,resources=nutservers;upsdevices;powermanagementclusters,verbs=get;list;watch
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
			setDegradedCondition(&agent.Status.Conditions, agent.Generation, rendered.UnavailableNodeCount > 0, reason, message)
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

	if err := r.Status().Update(ctx, &agent); err != nil {
		log.Error(err, "failed to update NodePowerAgent status")
		return ctrl.Result{}, err
	}

	return reconcileResult, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *NodePowerAgentReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&powerv1alpha1.NodePowerAgent{}).
		Owns(&appsv1.DaemonSet{}).
		Owns(&corev1.ConfigMap{}).
		Watches(&corev1.Pod{}, handler.EnqueueRequestsFromMapFunc(func(_ context.Context, obj client.Object) []reconcile.Request {
			agentName := obj.GetLabels()["power.zalud.io/nodepoweragent"]
			if agentName == "" {
				return nil
			}
			return []reconcile.Request{{NamespacedName: types.NamespacedName{Name: agentName}}}
		})).
		Owns(&corev1.Secret{}).
		Owns(&corev1.ServiceAccount{}).
		Owns(&networkingv1.NetworkPolicy{}).
		Named("nodepoweragent").
		Complete(r)
}
