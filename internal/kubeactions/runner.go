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

// Package kubeactions implements the effectful Kubernetes action-runner
// boundary for executor enforce mode.
package kubeactions

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	"sigs.k8s.io/controller-runtime/pkg/client"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/executor"
	"github.com/MichaelZalud18/nut-operator/internal/metrics"
	"github.com/MichaelZalud18/nut-operator/internal/nodeagent"
)

const defaultOperandNamespace = "power-system"

const (
	ActionNotify      = "Notify"
	ActionWait        = "Wait"
	ActionCordonNodes = "CordonNodes"
	ActionDrainNodes  = "DrainNodes"
	ActionScale       = "ScaleWorkload"
	ActionRunHook     = "RunHook"

	defaultHookCloudEventType      = "io.zalud.power.shutdown.hook.v1"
	paramReplicas                  = "replicas"
	paramScaleReplicas             = "scale.replicas"
	labelManagedBy                 = "app.kubernetes.io/managed-by"
	labelShutdownFlow              = "power.zalud.io/shutdownflow"
	labelShutdownFlowExecution     = "power.zalud.io/execution"
	labelShutdownFlowExecutorGroup = "power.zalud.io/executor-group"
)

// Runner performs approved Kubernetes mutations for executor enforce mode.
type Runner struct {
	Client client.Client
	Clock  func() time.Time

	// ManagerNamespace is the controller-manager's own install namespace (POD_NAMESPACE via the
	// downward API in config/manager/manager.yaml). Included in protectedNamespaces so a
	// ShutdownFlow's DrainNodes/ScaleWorkload actions can never evict or scale down the operator that
	// is executing them (F-30). Empty in envtest/unit tests, where there is no real manager pod to
	// protect.
	ManagerNamespace string

	// Recorder emits the Kubernetes Events behind the Notify action. Nil makes Notify
	// report Blocked rather than silently succeed: a notification nobody received is
	// not a notification delivered.
	Recorder events.EventRecorder

	// HTTPClient delivers HTTP ShutdownHooks. Nil uses http.DefaultClient with
	// per-request context deadlines supplied by the executor.
	HTTPClient *http.Client

	// SignalWritten, when set, is called once per node after its shutdown signal reaches the API
	// server. It is how the operator starts the clock on a halt it will only ever see the far end
	// of, since the node powers off and takes its own record with it.
	//
	// A plain function rather than an interface, and fired after the write rather than before it: a
	// Secret write the API server rejected asked no node to stop, and counting it would file
	// failures against machines that were never signalled. Nil is a build that records nothing,
	// which is what the unit tests use.
	SignalWritten func(node, shutdownFlow, executionID string, at time.Time)
}

// RunAction implements executor.ActionRunner. It is a thin instrumented wrapper around runAction: every
// executor action (real or dry-run) passes through here exactly once, making it the single choke point
// for the actuator's action-attempt metrics (F-3).
func (r Runner) RunAction(ctx context.Context, action executor.Action) (executor.ActionOutcome, error) {
	start := time.Now()
	outcome, err := r.runAction(ctx, action)

	mode := "Enforce"
	if action.DryRun {
		mode = "DryRun"
	}
	outcomeLabel := outcome.Outcome
	if outcomeLabel == "" {
		outcomeLabel = "Error"
	}
	metrics.ActuatorActionAttemptsTotal.WithLabelValues(action.Group.Action, mode, outcomeLabel).Inc()
	metrics.ActuatorActionDurationSeconds.WithLabelValues(action.Group.Action).Observe(time.Since(start).Seconds())

	return outcome, err
}

func (r Runner) runAction(ctx context.Context, action executor.Action) (executor.ActionOutcome, error) {
	if r.Client == nil {
		err := fmt.Errorf("kubernetes action runner requires a client")
		return blocked(err), err
	}
	switch action.Group.Action {
	case ActionWait:
		// Waiting is not a cluster mutation, so the executor performs it before the
		// action ever reaches this runner. Reaching here means the wait is already
		// served; there is nothing left to actuate.
		return executor.ActionOutcome{
			Outcome: executor.OutcomeSucceeded,
			Details: map[string]any{"action": action.Group.Action},
		}, nil
	case ActionNotify:
		return r.notify(ctx, action)
	case ActionScale:
		return r.scaleWorkloads(ctx, action)
	case ActionCordonNodes:
		return r.cordonNodes(ctx, action)
	case ActionDrainNodes:
		return r.drainNodes(ctx, action)
	case ActionRunHook:
		return r.runHook(ctx, action)
	case executor.ActionAgentShutdown:
		return r.agentShutdownHandoff(ctx, action)
	default:
		err := fmt.Errorf("unsupported Kubernetes shutdown action %q", action.Group.Action)
		return blocked(err), err
	}
}

// protectedNamespaces resolves the operand namespace of every NodePowerAgent in the cluster (F-14),
// plus the controller-manager's own namespace (F-30). Tier 0 excludes the power agent's own namespace
// from orchestrated flow targeting per OD-4/PL-22; this is the executor-side enforcement of that rule,
// structural rather than relying on incidental protections like DaemonSet-skip-on-drain. F-30 extends
// the same structural protection to the manager itself: without it, a ShutdownFlow group whose
// selector happens to match the manager's own node or namespace could evict or scale down the very
// process executing the flow.
func (r Runner) protectedNamespaces(ctx context.Context) (map[string]struct{}, error) {
	var agents powerv1alpha1.NodePowerAgentList
	if err := r.Client.List(ctx, &agents); err != nil {
		return nil, fmt.Errorf("list NodePowerAgents for self-exclusion: %w", err)
	}
	protected := make(map[string]struct{}, len(agents.Items)+1)
	if r.ManagerNamespace != "" {
		protected[r.ManagerNamespace] = struct{}{}
	}
	clusters := map[string]*powerv1alpha1.PowerManagementCluster{}
	for _, agent := range agents.Items {
		namespace, err := r.nodePowerAgentOperandNamespace(ctx, agent, clusters)
		if err != nil {
			return nil, err
		}
		protected[namespace] = struct{}{}
	}
	return protected, nil
}

func (r Runner) nodePowerAgentOperandNamespace(ctx context.Context, agent powerv1alpha1.NodePowerAgent, clusters map[string]*powerv1alpha1.PowerManagementCluster) (string, error) {
	if agent.Spec.Namespace != "" {
		return agent.Spec.Namespace, nil
	}
	if agent.Spec.ManagementClusterRef != nil && agent.Spec.ManagementClusterRef.Name != "" {
		name := agent.Spec.ManagementClusterRef.Name
		cluster, cached := clusters[name]
		if !cached {
			var fetched powerv1alpha1.PowerManagementCluster
			if err := r.Client.Get(ctx, types.NamespacedName{Name: name}, &fetched); err != nil {
				return "", fmt.Errorf("get PowerManagementCluster %q for self-exclusion: %w", name, err)
			}
			cluster = &fetched
			clusters[name] = cluster
		}
		if cluster.Spec.OperandNamespace != nil && cluster.Spec.OperandNamespace.Name != "" {
			return cluster.Spec.OperandNamespace.Name, nil
		}
	}
	return defaultOperandNamespace, nil
}

func (r Runner) scaleWorkloads(ctx context.Context, action executor.Action) (executor.ActionOutcome, error) {
	replicas, err := desiredReplicas(action.Group.Params)
	if err != nil {
		return blocked(err), err
	}
	protected, err := r.protectedNamespaces(ctx)
	if err != nil {
		return blocked(err), err
	}
	var changed int
	var visited int
	var excluded []string
	for _, target := range action.Group.SelectedTargets {
		if _, isProtected := protected[target.Namespace]; isProtected {
			excluded = append(excluded, target.Namespace+"/"+target.Name)
			continue
		}
		switch target.Kind {
		case "Deployment":
			updated, err := r.scaleDeployment(ctx, target, replicas)
			if err != nil {
				return blocked(err), err
			}
			visited++
			if updated {
				changed++
			}
		case "StatefulSet":
			updated, err := r.scaleStatefulSet(ctx, target, replicas)
			if err != nil {
				return blocked(err), err
			}
			visited++
			if updated {
				changed++
			}
		case "ReplicaSet":
			updated, err := r.scaleReplicaSet(ctx, target, replicas)
			if err != nil {
				return blocked(err), err
			}
			visited++
			if updated {
				changed++
			}
		}
	}
	if visited == 0 && len(excluded) == 0 {
		err := fmt.Errorf("ScaleWorkload selected no scalable targets")
		return blocked(err), err
	}
	details := map[string]any{
		"changed":           changed,
		"desiredReplicas":   replicas,
		"selectedWorkloads": visited,
	}
	if len(excluded) > 0 {
		sortStrings(excluded)
		details["selfExcluded"] = excluded
	}
	return executor.ActionOutcome{
		Outcome: executor.OutcomeSucceeded,
		Details: details,
	}, nil
}

func (r Runner) scaleDeployment(ctx context.Context, target executor.Target, replicas int32) (bool, error) {
	var workload appsv1.Deployment
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: target.Namespace, Name: target.Name}, &workload); err != nil {
		return false, fmt.Errorf("get Deployment %s/%s: %w", target.Namespace, target.Name, err)
	}
	if workload.Spec.Replicas != nil && *workload.Spec.Replicas == replicas {
		return false, nil
	}
	workload.Spec.Replicas = &replicas
	return true, r.Client.Update(ctx, &workload)
}

func (r Runner) scaleStatefulSet(ctx context.Context, target executor.Target, replicas int32) (bool, error) {
	var workload appsv1.StatefulSet
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: target.Namespace, Name: target.Name}, &workload); err != nil {
		return false, fmt.Errorf("get StatefulSet %s/%s: %w", target.Namespace, target.Name, err)
	}
	if workload.Spec.Replicas != nil && *workload.Spec.Replicas == replicas {
		return false, nil
	}
	workload.Spec.Replicas = &replicas
	return true, r.Client.Update(ctx, &workload)
}

func (r Runner) scaleReplicaSet(ctx context.Context, target executor.Target, replicas int32) (bool, error) {
	var workload appsv1.ReplicaSet
	if err := r.Client.Get(ctx, client.ObjectKey{Namespace: target.Namespace, Name: target.Name}, &workload); err != nil {
		return false, fmt.Errorf("get ReplicaSet %s/%s: %w", target.Namespace, target.Name, err)
	}
	if workload.Spec.Replicas != nil && *workload.Spec.Replicas == replicas {
		return false, nil
	}
	workload.Spec.Replicas = &replicas
	return true, r.Client.Update(ctx, &workload)
}

func (r Runner) cordonNodes(ctx context.Context, action executor.Action) (executor.ActionOutcome, error) {
	visited, changed, err := r.cordonSelectedNodes(ctx, action.Group.SelectedTargets)
	if err != nil {
		return blocked(err), err
	}
	if visited == 0 {
		err := fmt.Errorf("CordonNodes selected no Node targets")
		return blocked(err), err
	}
	return executor.ActionOutcome{
		Outcome: executor.OutcomeSucceeded,
		Details: map[string]any{
			"changed":       changed,
			"selectedNodes": visited,
		},
	}, nil
}

func (r Runner) drainNodes(ctx context.Context, action executor.Action) (executor.ActionOutcome, error) {
	visited, cordoned, err := r.cordonSelectedNodes(ctx, action.Group.SelectedTargets)
	if err != nil {
		return blocked(err), err
	}
	if visited == 0 {
		err := fmt.Errorf("DrainNodes selected no Node targets")
		return blocked(err), err
	}
	protected, err := r.protectedNamespaces(ctx)
	if err != nil {
		return blocked(err), err
	}
	evicted := 0
	var excludedNamespaces []string
	for _, target := range action.Group.SelectedTargets {
		if target.Kind != "Node" || target.Name == "" {
			continue
		}
		count, skipped, err := r.evictPodsOnNode(ctx, target.Name, protected)
		if err != nil {
			return blocked(err), err
		}
		evicted += count
		excludedNamespaces = append(excludedNamespaces, skipped...)
	}
	details := map[string]any{
		"cordoned":      cordoned,
		"evictedPods":   evicted,
		"selectedNodes": visited,
	}
	if len(excludedNamespaces) > 0 {
		sortStrings(excludedNamespaces)
		details["selfExcludedNamespaces"] = dedupeStrings(excludedNamespaces)
	}
	return executor.ActionOutcome{
		Outcome: executor.OutcomeSucceeded,
		Details: details,
	}, nil
}

func (r Runner) cordonSelectedNodes(ctx context.Context, targets []executor.Target) (visited, changed int, err error) {
	for _, target := range targets {
		if target.Kind != "Node" || target.Name == "" {
			continue
		}
		var node corev1.Node
		if err := r.Client.Get(ctx, client.ObjectKey{Name: target.Name}, &node); err != nil {
			return visited, changed, fmt.Errorf("get Node %q: %w", target.Name, err)
		}
		visited++
		if node.Spec.Unschedulable {
			continue
		}
		node.Spec.Unschedulable = true
		if err := r.Client.Update(ctx, &node); err != nil {
			return visited, changed, fmt.Errorf("cordon Node %q: %w", target.Name, err)
		}
		changed++
	}
	return visited, changed, nil
}

func (r Runner) evictPodsOnNode(ctx context.Context, nodeName string, protected map[string]struct{}) (int, []string, error) {
	var pods corev1.PodList
	if err := r.Client.List(ctx, &pods); err != nil {
		return 0, nil, fmt.Errorf("list Pods for drain on Node %q: %w", nodeName, err)
	}
	evicted := 0
	var skippedNamespaces []string
	for _, pod := range pods.Items {
		if pod.Spec.NodeName != nodeName || !evictablePod(pod) {
			continue
		}
		if _, isProtected := protected[pod.Namespace]; isProtected {
			skippedNamespaces = append(skippedNamespaces, pod.Namespace)
			continue
		}
		eviction := &policyv1.Eviction{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: pod.Namespace,
				Name:      pod.Name,
			},
		}
		if err := r.Client.SubResource("eviction").Create(ctx, &pod, eviction); err != nil {
			if apierrors.IsNotFound(err) {
				continue
			}
			return evicted, skippedNamespaces, fmt.Errorf("evict Pod %s/%s: %w", pod.Namespace, pod.Name, err)
		}
		evicted++
	}
	return evicted, skippedNamespaces, nil
}

func evictablePod(pod corev1.Pod) bool {
	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return false
	}
	if pod.Annotations["kubernetes.io/config.mirror"] != "" {
		return false
	}
	for _, owner := range pod.OwnerReferences {
		if owner.APIVersion == "apps/v1" && owner.Kind == "DaemonSet" {
			return false
		}
	}
	return true
}

// notify emits a Kubernetes Event against the ShutdownFlow.
//
// Events are the transport because they need no configuration to exist. A Notify
// backed by a webhook or a chat integration would be inert until someone wired it
// up, and an inert notification during an outage is worse than none — the flow
// author believes they will be told.
//
// The event lands on the ShutdownFlow rather than on the targets, because the
// message is about the flow's progress, and because a notification attached to a
// workload that is about to be scaled to zero is a notification nobody reads.
func (r Runner) notify(ctx context.Context, action executor.Action) (executor.ActionOutcome, error) {
	message := action.Group.Params["message"]
	if message == "" {
		message = fmt.Sprintf("shutdown flow %q reached group %q", action.ShutdownFlow, action.Group.Name)
	}
	reason := action.Group.Params["reason"]
	if reason == "" {
		reason = "ShutdownFlowNotify"
	}

	if r.Recorder == nil {
		// No recorder configured. Reported rather than silently succeeding, so the action
		// attempt shows that the notification did not go anywhere.
		return executor.ActionOutcome{
			Outcome: executor.OutcomeBlocked,
			Error:   "no event recorder configured for Notify",
			Details: map[string]any{"action": ActionNotify, "message": message},
		}, nil
	}

	var flow powerv1alpha1.ShutdownFlow
	if err := r.Client.Get(ctx, types.NamespacedName{Name: action.ShutdownFlow}, &flow); err != nil {
		return blocked(err), err
	}
	// The new events API carries an explicit action alongside the reason, so the group
	// name goes there: it is what was being done when the note was emitted.
	r.Recorder.Eventf(&flow, nil, corev1.EventTypeNormal, reason, action.Group.Name, "%s", message)

	return executor.ActionOutcome{
		Outcome: executor.OutcomeSucceeded,
		Details: map[string]any{
			"action":  ActionNotify,
			"reason":  reason,
			"message": message,
		},
	}, nil
}

func (r Runner) runHook(ctx context.Context, action executor.Action) (executor.ActionOutcome, error) {
	if action.Group.HookRef == nil || action.Group.HookRef.Namespace == "" || action.Group.HookRef.Name == "" {
		err := fmt.Errorf("RunHook requires hookRef namespace and name")
		return blocked(err), err
	}
	var hook powerv1alpha1.ShutdownHook
	hookKey := types.NamespacedName{Namespace: action.Group.HookRef.Namespace, Name: action.Group.HookRef.Name}
	if err := r.Client.Get(ctx, hookKey, &hook); err != nil {
		return blocked(err), fmt.Errorf("get ShutdownHook %s/%s: %w", hookKey.Namespace, hookKey.Name, err)
	}

	invocation := hook.Spec.Invocation
	rehearsalDeclared := false
	if action.DryRun {
		if hook.Spec.DryRun == nil {
			return r.simulatedHookOutcome(ctx, action, &hook, invocation)
		}
		invocation = *hook.Spec.DryRun
		rehearsalDeclared = true
	}

	switch hookTransport(invocation.Transport) {
	case powerv1alpha1.ShutdownHookTransportHTTP:
		return r.runHTTPHook(ctx, action, &hook, invocation, rehearsalDeclared)
	case powerv1alpha1.ShutdownHookTransportKubernetesObject:
		return r.runKubernetesObjectHook(ctx, action, &hook, invocation, rehearsalDeclared)
	default:
		err := fmt.Errorf("unsupported ShutdownHook transport %q", invocation.Transport)
		return blocked(err), err
	}
}

func (r Runner) simulatedHookOutcome(ctx context.Context, action executor.Action, hook *powerv1alpha1.ShutdownHook, invocation powerv1alpha1.ShutdownHookInvocationSpec) (executor.ActionOutcome, error) {
	if hookTransport(invocation.Transport) == powerv1alpha1.ShutdownHookTransportHTTP && invocation.HTTP != nil {
		parsed, err := url.Parse(invocation.HTTP.URL)
		if err != nil || !parsed.IsAbs() || parsed.Hostname() == "" {
			if err == nil {
				err = fmt.Errorf("URL must be absolute and include a host")
			}
			return blocked(err), fmt.Errorf("invalid ShutdownHook URL %q: %w", invocation.HTTP.URL, err)
		}
		cluster, err := r.managementClusterForHook(ctx, action.ShutdownFlow)
		if err != nil {
			return blocked(err), err
		}
		if !hookURLAllowed(parsed, cluster.Spec.Hooks.AllowedEndpoints) {
			err := fmt.Errorf("ShutdownHook %s/%s URL %q is not allowed by PowerManagementCluster %q",
				hook.Namespace, hook.Name, invocation.HTTP.URL, cluster.Name)
			return blocked(err), err
		}
	}
	request, err := r.hookRequestSummary(action, hook, invocation)
	if err != nil {
		return blocked(err), err
	}
	return executor.ActionOutcome{
		Outcome: executor.OutcomeSimulated,
		Details: map[string]any{
			"action":              ActionRunHook,
			"dryRun":              true,
			"hook":                hook.Namespace + "/" + hook.Name,
			"rehearsalDeclared":   false,
			"wouldSendInvocation": request,
		},
	}, nil
}

func (r Runner) runHTTPHook(ctx context.Context, action executor.Action, hook *powerv1alpha1.ShutdownHook, invocation powerv1alpha1.ShutdownHookInvocationSpec, rehearsalDeclared bool) (executor.ActionOutcome, error) {
	if invocation.HTTP == nil {
		err := fmt.Errorf("ShutdownHook %s/%s HTTP transport requires spec.http", hook.Namespace, hook.Name)
		return blocked(err), err
	}
	parsed, err := url.Parse(invocation.HTTP.URL)
	if err != nil || !parsed.IsAbs() || parsed.Hostname() == "" {
		if err == nil {
			err = fmt.Errorf("URL must be absolute and include a host")
		}
		return blocked(err), fmt.Errorf("invalid ShutdownHook URL %q: %w", invocation.HTTP.URL, err)
	}
	cluster, err := r.managementClusterForHook(ctx, action.ShutdownFlow)
	if err != nil {
		return blocked(err), err
	}
	if !hookURLAllowed(parsed, cluster.Spec.Hooks.AllowedEndpoints) {
		err := fmt.Errorf("ShutdownHook %s/%s URL %q is not allowed by PowerManagementCluster %q",
			hook.Namespace, hook.Name, invocation.HTTP.URL, cluster.Name)
		return blocked(err), err
	}

	body := r.cloudEventBody(action, hook, invocation.HTTP, rehearsalDeclared)
	encoded, err := json.Marshal(body)
	if err != nil {
		return blocked(err), fmt.Errorf("encode ShutdownHook CloudEvent: %w", err)
	}
	method := invocation.HTTP.Method
	if method == "" {
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, invocation.HTTP.URL, bytes.NewReader(encoded))
	if err != nil {
		return blocked(err), err
	}
	req.Header.Set("Content-Type", "application/cloudevents+json")
	for _, header := range invocation.HTTP.Headers {
		req.Header.Set(header.Name, header.Value)
	}
	if err := r.applySecretHeaders(ctx, req, invocation.HTTP.SecretHeaders); err != nil {
		return blocked(err), err
	}

	response, err := r.httpClient().Do(req)
	if err != nil {
		return blocked(err), fmt.Errorf("deliver ShutdownHook %s/%s to %s: %w", hook.Namespace, hook.Name, invocation.HTTP.URL, err)
	}
	defer func() {
		_ = response.Body.Close()
	}()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 2048))
		err := fmt.Errorf("deliver ShutdownHook %s/%s to %s: status %d %s",
			hook.Namespace, hook.Name, invocation.HTTP.URL, response.StatusCode, strings.TrimSpace(string(body)))
		return blocked(err), err
	}

	return executor.ActionOutcome{
		Outcome: executor.OutcomeSucceeded,
		Details: map[string]any{
			"action":            ActionRunHook,
			"transport":         string(powerv1alpha1.ShutdownHookTransportHTTP),
			"dryRun":            action.DryRun,
			"hook":              hook.Namespace + "/" + hook.Name,
			"rehearsalDeclared": rehearsalDeclared,
			"statusCode":        response.StatusCode,
			"url":               redactedURL(invocation.HTTP.URL),
		},
	}, nil
}

func (r Runner) runKubernetesObjectHook(ctx context.Context, action executor.Action, hook *powerv1alpha1.ShutdownHook, invocation powerv1alpha1.ShutdownHookInvocationSpec, rehearsalDeclared bool) (executor.ActionOutcome, error) {
	if invocation.KubernetesObject == nil || len(invocation.KubernetesObject.Object.Raw) == 0 {
		err := fmt.Errorf("ShutdownHook %s/%s KubernetesObject transport requires spec.kubernetesObject.object", hook.Namespace, hook.Name)
		return blocked(err), err
	}
	var object map[string]any
	if err := json.Unmarshal(invocation.KubernetesObject.Object.Raw, &object); err != nil {
		return blocked(err), fmt.Errorf("decode ShutdownHook Kubernetes object: %w", err)
	}
	apiVersion, _ := object["apiVersion"].(string)
	kind, _ := object["kind"].(string)
	if apiVersion == "" || kind == "" {
		err := fmt.Errorf("ShutdownHook %s/%s Kubernetes object requires apiVersion and kind", hook.Namespace, hook.Name)
		return blocked(err), err
	}
	obj := &unstructured.Unstructured{Object: object}
	obj.SetGroupVersionKind(schema.FromAPIVersionAndKind(apiVersion, kind))
	annotateHookObject(obj, action, hook, r.now())
	if err := r.Client.Create(ctx, obj); err != nil {
		return blocked(err), fmt.Errorf("create ShutdownHook Kubernetes object %s %s/%s: %w",
			obj.GroupVersionKind().String(), obj.GetNamespace(), obj.GetName(), err)
	}
	return executor.ActionOutcome{
		Outcome: executor.OutcomeSucceeded,
		Details: map[string]any{
			"action":            ActionRunHook,
			"transport":         string(powerv1alpha1.ShutdownHookTransportKubernetesObject),
			"dryRun":            action.DryRun,
			"hook":              hook.Namespace + "/" + hook.Name,
			"rehearsalDeclared": rehearsalDeclared,
			"created": map[string]string{
				"apiVersion": obj.GetAPIVersion(),
				"kind":       obj.GetKind(),
				"namespace":  obj.GetNamespace(),
				"name":       obj.GetName(),
			},
		},
	}, nil
}

func (r Runner) hookRequestSummary(action executor.Action, hook *powerv1alpha1.ShutdownHook, invocation powerv1alpha1.ShutdownHookInvocationSpec) (map[string]any, error) {
	switch hookTransport(invocation.Transport) {
	case powerv1alpha1.ShutdownHookTransportHTTP:
		if invocation.HTTP == nil {
			return nil, fmt.Errorf("ShutdownHook %s/%s HTTP transport requires spec.http", hook.Namespace, hook.Name)
		}
		return map[string]any{
			"transport": string(powerv1alpha1.ShutdownHookTransportHTTP),
			"method":    hookHTTPMethod(invocation.HTTP.Method),
			"url":       redactedURL(invocation.HTTP.URL),
			"body":      r.cloudEventBody(action, hook, invocation.HTTP, false),
		}, nil
	case powerv1alpha1.ShutdownHookTransportKubernetesObject:
		if invocation.KubernetesObject == nil || len(invocation.KubernetesObject.Object.Raw) == 0 {
			return nil, fmt.Errorf("ShutdownHook %s/%s KubernetesObject transport requires spec.kubernetesObject.object", hook.Namespace, hook.Name)
		}
		var object map[string]any
		if err := json.Unmarshal(invocation.KubernetesObject.Object.Raw, &object); err != nil {
			return nil, fmt.Errorf("decode ShutdownHook Kubernetes object: %w", err)
		}
		apiVersion, _ := object["apiVersion"].(string)
		kind, _ := object["kind"].(string)
		return map[string]any{
			"transport":  string(powerv1alpha1.ShutdownHookTransportKubernetesObject),
			"apiVersion": apiVersion,
			"kind":       kind,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported ShutdownHook transport %q", invocation.Transport)
	}
}

func (r Runner) applySecretHeaders(ctx context.Context, req *http.Request, headers []powerv1alpha1.ShutdownHookHTTPSecretHeader) error {
	for _, header := range headers {
		var secret corev1.Secret
		key := types.NamespacedName{Namespace: header.ValueFrom.Namespace, Name: header.ValueFrom.Name}
		if err := r.Client.Get(ctx, key, &secret); err != nil {
			return fmt.Errorf("get Secret %s/%s for ShutdownHook header %q: %w", key.Namespace, key.Name, header.Name, err)
		}
		value, exists := secret.Data[header.ValueFrom.Key]
		if !exists {
			return fmt.Errorf("secret %s/%s does not contain key %q for ShutdownHook header %q",
				key.Namespace, key.Name, header.ValueFrom.Key, header.Name)
		}
		req.Header.Set(header.Name, string(value))
	}
	return nil
}

func (r Runner) managementClusterForHook(ctx context.Context, flowName string) (*powerv1alpha1.PowerManagementCluster, error) {
	var flow powerv1alpha1.ShutdownFlow
	if err := r.Client.Get(ctx, types.NamespacedName{Name: flowName}, &flow); err != nil {
		return nil, fmt.Errorf("get ShutdownFlow %q for hook policy: %w", flowName, err)
	}
	if flow.Spec.ManagementClusterRef == nil || flow.Spec.ManagementClusterRef.Name == "" {
		return nil, fmt.Errorf("ShutdownFlow %q requires spec.managementClusterRef for RunHook endpoint policy", flowName)
	}
	var cluster powerv1alpha1.PowerManagementCluster
	if err := r.Client.Get(ctx, types.NamespacedName{Name: flow.Spec.ManagementClusterRef.Name}, &cluster); err != nil {
		return nil, fmt.Errorf("get PowerManagementCluster %q for hook policy: %w", flow.Spec.ManagementClusterRef.Name, err)
	}
	return &cluster, nil
}

func (r Runner) cloudEventBody(action executor.Action, hook *powerv1alpha1.ShutdownHook, httpSpec *powerv1alpha1.ShutdownHookHTTPSpec, rehearsalDeclared bool) map[string]any {
	observedAt := r.now()
	eventType := httpSpec.CloudEventType
	if eventType == "" {
		eventType = defaultHookCloudEventType
	}
	source := httpSpec.CloudEventSource
	if source == "" {
		source = "urn:power.zalud.io:shutdownflow:" + action.ShutdownFlow
	}
	return map[string]any{
		"specversion":     "1.0",
		"id":              fmt.Sprintf("%s-%s-%d", action.ExecutionID, action.Group.Name, observedAt.UnixNano()),
		"source":          source,
		"type":            eventType,
		"time":            observedAt.UTC().Format(time.RFC3339Nano),
		"datacontenttype": "application/json",
		"data": map[string]any{
			"executionID":        action.ExecutionID,
			"planHash":           action.PlanConfigHash,
			"shutdownFlow":       action.ShutdownFlow,
			"group":              action.Group.Name,
			"waveIndex":          action.WaveIndex,
			"triggerReason":      action.TriggerReason,
			"dryRun":             action.DryRun,
			"rehearsalDeclared":  rehearsalDeclared,
			"selectedUPSDevices": append([]string(nil), action.SelectedUPSDevices...),
			"hook": map[string]string{
				"namespace": hook.Namespace,
				"name":      hook.Name,
			},
			"power": map[string]any{
				"onBattery":      action.PowerObservation.OnBattery,
				"lowBattery":     action.PowerObservation.LowBattery,
				"runtimeSeconds": action.PowerObservation.RuntimeSeconds,
				"runtimeTrusted": action.PowerObservation.RuntimeTrusted,
			},
			"params":     copyStringMap(action.Group.Params),
			"extensions": copyStringMap(httpSpec.Data),
		},
	}
}

func hookTransport(transport powerv1alpha1.ShutdownHookTransport) powerv1alpha1.ShutdownHookTransport {
	if transport == "" {
		return powerv1alpha1.ShutdownHookTransportHTTP
	}
	return transport
}

func hookHTTPMethod(method string) string {
	if method == "" {
		return http.MethodPost
	}
	return method
}

func (r Runner) httpClient() *http.Client {
	if r.HTTPClient != nil {
		return r.HTTPClient
	}
	return http.DefaultClient
}

func hookURLAllowed(parsed *url.URL, allowed []powerv1alpha1.PowerHookEndpointAllowlistEntry) bool {
	for _, endpoint := range allowed {
		if parsed.Scheme != endpoint.Scheme {
			continue
		}
		if !strings.EqualFold(parsed.Hostname(), endpoint.Host) {
			continue
		}
		if effectiveURLPort(parsed) != allowedEndpointPort(endpoint) {
			continue
		}
		path := parsed.Path
		if path == "" {
			path = "/"
		}
		if endpoint.PathPrefix != "" && !strings.HasPrefix(path, endpoint.PathPrefix) {
			continue
		}
		return true
	}
	return false
}

func effectiveURLPort(parsed *url.URL) int32 {
	if port := parsed.Port(); port != "" {
		value, err := strconv.Atoi(port)
		if err == nil {
			return int32(value)
		}
	}
	switch parsed.Scheme {
	case "http":
		return 80
	case "https":
		return 443
	default:
		return 0
	}
}

func allowedEndpointPort(endpoint powerv1alpha1.PowerHookEndpointAllowlistEntry) int32 {
	if endpoint.Port != nil {
		return *endpoint.Port
	}
	switch endpoint.Scheme {
	case "http":
		return 80
	case "https":
		return 443
	default:
		return 0
	}
}

func redactedURL(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User == nil {
		return raw
	}
	parsed.User = url.UserPassword(parsed.User.Username(), "redacted")
	return parsed.String()
}

func annotateHookObject(obj *unstructured.Unstructured, action executor.Action, hook *powerv1alpha1.ShutdownHook, observedAt time.Time) {
	labels := obj.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[labelManagedBy] = "nut-operator"
	labels[labelShutdownFlow] = labelValue(action.ShutdownFlow)
	labels[labelShutdownFlowExecution] = labelValue(action.ExecutionID)
	labels[labelShutdownFlowExecutorGroup] = labelValue(action.Group.Name)
	obj.SetLabels(labels)

	annotations := obj.GetAnnotations()
	if annotations == nil {
		annotations = map[string]string{}
	}
	annotations["power.zalud.io/execution-id"] = action.ExecutionID
	annotations["power.zalud.io/executor-group"] = action.Group.Name
	annotations["power.zalud.io/hook"] = hook.Namespace + "/" + hook.Name
	annotations["power.zalud.io/observed-at"] = observedAt.UTC().Format(time.RFC3339Nano)
	annotations["power.zalud.io/plan-hash"] = action.PlanConfigHash
	annotations["power.zalud.io/shutdownflow"] = action.ShutdownFlow
	obj.SetAnnotations(annotations)
}

func (r Runner) agentShutdownHandoff(ctx context.Context, action executor.Action) (executor.ActionOutcome, error) {
	if len(action.Group.NodeReleases) == 0 {
		err := fmt.Errorf("AgentShutdown requires at least one selected node release")
		return blocked(err), err
	}
	if action.ExecutionID == "" || action.ShutdownFlow == "" || action.PlanConfigHash == "" {
		err := fmt.Errorf("AgentShutdown requires execution ID, shutdown flow, and plan config hash")
		return blocked(err), err
	}
	observedAt := r.now()
	updatedSecrets := map[string]struct{}{}
	for _, release := range action.Group.NodeReleases {
		if release.NodeName == "" || release.SignalSecretNamespace == "" || release.SignalSecretName == "" || release.SignalSecretKey == "" {
			err := fmt.Errorf("AgentShutdown release for node %q requires signal Secret namespace, name, and key", release.NodeName)
			return blocked(err), err
		}
		payload := nodeagent.ShutdownSignal{
			ExecutionID:        action.ExecutionID,
			NodeName:           release.NodeName,
			PlanConfigHash:     action.PlanConfigHash,
			Reason:             "ReleaseApproved",
			SelectedUPSDevices: append([]string(nil), action.SelectedUPSDevices...),
			ShutdownFlow:       action.ShutdownFlow,
			Timestamp:          observedAt.UTC().Format(time.RFC3339Nano),
			// EX-31: the tier is already past its budget, so the actuator halts without waiting on
			// sync(2). Bound to the overrun state rather than exposed as an agent setting, because
			// the skip inverts its own outcome -- the nodes rushed to save time come back with the
			// most to recover -- and only the thing running the plan knows the plan is late.
			SkipSync: action.TierOverrunning,
		}
		encoded, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return blocked(err), fmt.Errorf("encode AgentShutdown signal for node %q: %w", release.NodeName, err)
		}
		encoded = append(encoded, '\n')
		if err := r.upsertSignalSecret(ctx, action, release, encoded); err != nil {
			return blocked(err), err
		}
		if r.SignalWritten != nil {
			r.SignalWritten(release.NodeName, action.ShutdownFlow, action.ExecutionID, observedAt)
		}
		updatedSecrets[release.SignalSecretNamespace+"/"+release.SignalSecretName] = struct{}{}
	}
	return executor.ActionOutcome{
		Outcome: executor.OutcomeSucceeded,
		Details: map[string]any{
			"handoff":       "ProjectedSecretSignal",
			"nodeReleases":  len(action.Group.NodeReleases),
			"signalSecrets": len(updatedSecrets),
			// Recorded on the operator side because the node's own record of it does not survive:
			// the actuator logs the skip and then halts, taking the log with it. This is the
			// durable half, and it is written before the nodes it describes are asked to stop.
			"syncSkipped": action.TierOverrunning,
		},
	}, nil
}

func (r Runner) upsertSignalSecret(ctx context.Context, action executor.Action, release executor.NodeRelease, payload []byte) error {
	key := client.ObjectKey{Namespace: release.SignalSecretNamespace, Name: release.SignalSecretName}
	var secret corev1.Secret
	if err := r.Client.Get(ctx, key, &secret); err != nil {
		if !apierrors.IsNotFound(err) {
			return fmt.Errorf("get signal Secret %s/%s: %w", key.Namespace, key.Name, err)
		}
		secret = corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{
				Namespace: key.Namespace,
				Name:      key.Name,
				Labels:    signalSecretLabels(action, release),
			},
			Type: corev1.SecretTypeOpaque,
			Data: map[string][]byte{release.SignalSecretKey: payload},
		}
		if err := r.Client.Create(ctx, &secret); err != nil {
			return fmt.Errorf("create signal Secret %s/%s: %w", key.Namespace, key.Name, err)
		}
		return nil
	}
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	if secret.Labels == nil {
		secret.Labels = map[string]string{}
	}
	for label, value := range signalSecretLabels(action, release) {
		secret.Labels[label] = value
	}
	secret.Type = corev1.SecretTypeOpaque
	secret.Data[release.SignalSecretKey] = payload
	if err := r.Client.Update(ctx, &secret); err != nil {
		return fmt.Errorf("update signal Secret %s/%s key %q: %w", key.Namespace, key.Name, release.SignalSecretKey, err)
	}
	return nil
}

func signalSecretLabels(action executor.Action, release executor.NodeRelease) map[string]string {
	return map[string]string{
		labelManagedBy:                  "nut-operator",
		labelShutdownFlow:               labelValue(action.ShutdownFlow),
		labelShutdownFlowExecution:      labelValue(action.ExecutionID),
		"power.zalud.io/nodepoweragent": labelValue(release.NodePowerAgent),
	}
}

func desiredReplicas(params map[string]string) (int32, error) {
	value := params[paramScaleReplicas]
	if value == "" {
		value = params[paramReplicas]
	}
	if value == "" {
		return 0, nil
	}
	replicas, err := strconv.ParseInt(value, 10, 32)
	if err != nil || replicas < 0 {
		return 0, fmt.Errorf("replicas must be a non-negative integer")
	}
	return int32(replicas), nil
}

func blocked(err error) executor.ActionOutcome {
	return executor.ActionOutcome{
		Outcome: executor.OutcomeBlocked,
		Error:   err.Error(),
	}
}

func copyStringMap(input map[string]string) map[string]string {
	if input == nil {
		return nil
	}
	out := make(map[string]string, len(input))
	for key, value := range input {
		out[key] = value
	}
	return out
}

func (r Runner) now() time.Time {
	if r.Clock != nil {
		return r.Clock().UTC()
	}
	return time.Now().UTC()
}

func labelValue(value string) string {
	if value == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range value {
		valid := (r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			r == '-' ||
			r == '_' ||
			r == '.'
		if valid {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-_.")
	if out == "" {
		return "unknown"
	}
	if len(out) > 63 {
		out = strings.Trim(out[:63], "-_.")
	}
	if out == "" {
		return "unknown"
	}
	return out
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		value := values[i]
		j := i - 1
		for ; j >= 0 && values[j] > value; j-- {
			values[j+1] = values[j]
		}
		values[j+1] = value
	}
}

// dedupeStrings assumes values is already sorted.
func dedupeStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for i, value := range values {
		if i == 0 || value != values[i-1] {
			out = append(out, value)
		}
	}
	return out
}
