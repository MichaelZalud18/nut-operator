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
	"context"
	"fmt"
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
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/MichaelZalud18/nut-operator/internal/executor"
)

const (
	ActionNotify      = "Notify"
	ActionWait        = "Wait"
	ActionGate        = "Gate"
	ActionCordonNodes = "CordonNodes"
	ActionDrainNodes  = "DrainNodes"
	ActionScale       = "ScaleWorkload"
	ActionWorkflow    = "RunWorkflow"

	paramReplicas                  = "replicas"
	paramScaleReplicas             = "scale.replicas"
	paramWorkflowAPIVersion        = "workflow.apiVersion"
	paramWorkflowKind              = "workflow.kind"
	paramWorkflowNamespace         = "workflow.namespace"
	paramWorkflowName              = "workflow.name"
	paramWorkflowGenerateName      = "workflow.generateName"
	paramWorkflowTemplateRef       = "workflow.templateRef"
	paramWorkflowTemplateRefKind   = "workflow.templateRef.kind"
	paramWorkflowEntrypoint        = "workflow.entrypoint"
	paramWorkflowServiceAccount    = "workflow.serviceAccountName"
	paramWorkflowParameterPrefix   = "workflow.parameter."
	labelManagedBy                 = "app.kubernetes.io/managed-by"
	labelShutdownFlow              = "power.zalud.io/shutdownflow"
	labelShutdownFlowExecution     = "power.zalud.io/execution"
	labelShutdownFlowExecutorGroup = "power.zalud.io/executor-group"
)

// Runner performs approved Kubernetes mutations for executor enforce mode.
type Runner struct {
	Client client.Client
	Clock  func() time.Time
}

// RunAction implements executor.ActionRunner.
func (r Runner) RunAction(ctx context.Context, action executor.Action) (executor.ActionOutcome, error) {
	if r.Client == nil {
		err := fmt.Errorf("Kubernetes action runner requires a client")
		return blocked(err), err
	}
	switch action.Group.Action {
	case ActionNotify, ActionWait, ActionGate:
		return executor.ActionOutcome{
			Outcome: executor.OutcomeSucceeded,
			Details: map[string]any{
				"action": action.Group.Action,
				"noop":   true,
			},
		}, nil
	case ActionScale:
		return r.scaleWorkloads(ctx, action)
	case ActionCordonNodes:
		return r.cordonNodes(ctx, action)
	case ActionDrainNodes:
		return r.drainNodes(ctx, action)
	case ActionWorkflow:
		return r.runWorkflow(ctx, action)
	case executor.ActionAgentShutdown:
		return r.agentShutdownHandoff(action)
	default:
		err := fmt.Errorf("unsupported Kubernetes shutdown action %q", action.Group.Action)
		return blocked(err), err
	}
}

func (r Runner) scaleWorkloads(ctx context.Context, action executor.Action) (executor.ActionOutcome, error) {
	replicas, err := desiredReplicas(action.Group.Params)
	if err != nil {
		return blocked(err), err
	}
	var changed int
	var visited int
	for _, target := range action.Group.SelectedTargets {
		target := target
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
	if visited == 0 {
		err := fmt.Errorf("ScaleWorkload selected no scalable targets")
		return blocked(err), err
	}
	return executor.ActionOutcome{
		Outcome: executor.OutcomeSucceeded,
		Details: map[string]any{
			"changed":           changed,
			"desiredReplicas":   replicas,
			"selectedWorkloads": visited,
		},
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
	evicted := 0
	for _, target := range action.Group.SelectedTargets {
		if target.Kind != "Node" || target.Name == "" {
			continue
		}
		count, err := r.evictPodsOnNode(ctx, target.Name)
		if err != nil {
			return blocked(err), err
		}
		evicted += count
	}
	return executor.ActionOutcome{
		Outcome: executor.OutcomeSucceeded,
		Details: map[string]any{
			"cordoned":      cordoned,
			"evictedPods":   evicted,
			"selectedNodes": visited,
		},
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

func (r Runner) evictPodsOnNode(ctx context.Context, nodeName string) (int, error) {
	var pods corev1.PodList
	if err := r.Client.List(ctx, &pods); err != nil {
		return 0, fmt.Errorf("list Pods for drain on Node %q: %w", nodeName, err)
	}
	evicted := 0
	for _, pod := range pods.Items {
		if pod.Spec.NodeName != nodeName || !evictablePod(pod) {
			continue
		}
		pod := pod
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
			return evicted, fmt.Errorf("evict Pod %s/%s: %w", pod.Namespace, pod.Name, err)
		}
		evicted++
	}
	return evicted, nil
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

func (r Runner) runWorkflow(ctx context.Context, action executor.Action) (executor.ActionOutcome, error) {
	namespaces := workflowNamespaces(action.Group.SelectedTargets, action.Group.Params)
	if len(namespaces) == 0 {
		err := fmt.Errorf("RunWorkflow requires namespace targets or %s", paramWorkflowNamespace)
		return blocked(err), err
	}
	if action.Group.Params[paramWorkflowTemplateRef] == "" {
		err := fmt.Errorf("RunWorkflow requires %s", paramWorkflowTemplateRef)
		return blocked(err), err
	}

	created := 0
	for _, namespace := range namespaces {
		workflow := workflowObject(action, namespace, r.now())
		if err := r.Client.Create(ctx, workflow); err != nil {
			return blocked(err), fmt.Errorf("create workflow hook in namespace %q: %w", namespace, err)
		}
		created++
	}
	return executor.ActionOutcome{
		Outcome: executor.OutcomeSucceeded,
		Details: map[string]any{
			"createdWorkflows": created,
			"namespaces":       namespaces,
		},
	}, nil
}

func workflowObject(action executor.Action, namespace string, observedAt time.Time) *unstructured.Unstructured {
	params := action.Group.Params
	apiVersion := params[paramWorkflowAPIVersion]
	if apiVersion == "" {
		apiVersion = "argoproj.io/v1alpha1"
	}
	kind := params[paramWorkflowKind]
	if kind == "" {
		kind = "Workflow"
	}
	workflow := &unstructured.Unstructured{}
	workflow.SetGroupVersionKind(schema.FromAPIVersionAndKind(apiVersion, kind))
	workflow.SetNamespace(namespace)
	if name := params[paramWorkflowName]; name != "" {
		workflow.SetName(name)
	} else {
		generateName := params[paramWorkflowGenerateName]
		if generateName == "" {
			generateName = dnsPrefix(action.ShutdownFlow + "-" + action.Group.Name + "-")
		}
		workflow.SetGenerateName(generateName)
	}
	workflow.SetLabels(map[string]string{
		labelManagedBy:                 "nut-operator",
		labelShutdownFlow:              labelValue(action.ShutdownFlow),
		labelShutdownFlowExecution:     labelValue(action.ExecutionID),
		labelShutdownFlowExecutorGroup: labelValue(action.Group.Name),
	})
	workflow.SetAnnotations(map[string]string{
		"power.zalud.io/execution-id":   action.ExecutionID,
		"power.zalud.io/executor-group": action.Group.Name,
		"power.zalud.io/observed-at":    observedAt.UTC().Format(time.RFC3339Nano),
		"power.zalud.io/plan-hash":      action.PlanConfigHash,
		"power.zalud.io/shutdownflow":   action.ShutdownFlow,
	})

	spec := map[string]any{
		"workflowTemplateRef": map[string]any{
			"name": params[paramWorkflowTemplateRef],
		},
	}
	if templateKind := params[paramWorkflowTemplateRefKind]; templateKind != "" {
		spec["workflowTemplateRef"].(map[string]any)["kind"] = templateKind
	}
	if entrypoint := params[paramWorkflowEntrypoint]; entrypoint != "" {
		spec["entrypoint"] = entrypoint
	}
	if serviceAccount := params[paramWorkflowServiceAccount]; serviceAccount != "" {
		spec["serviceAccountName"] = serviceAccount
	}
	if parameters := workflowParameters(params); len(parameters) > 0 {
		spec["arguments"] = map[string]any{"parameters": parameters}
	}
	workflow.Object["spec"] = spec
	return workflow
}

func workflowNamespaces(targets []executor.Target, params map[string]string) []string {
	seen := map[string]struct{}{}
	var namespaces []string
	if namespace := params[paramWorkflowNamespace]; namespace != "" {
		seen[namespace] = struct{}{}
		namespaces = append(namespaces, namespace)
	}
	for _, target := range targets {
		if target.Kind != "Namespace" || target.Name == "" {
			continue
		}
		if _, exists := seen[target.Name]; exists {
			continue
		}
		seen[target.Name] = struct{}{}
		namespaces = append(namespaces, target.Name)
	}
	return namespaces
}

func workflowParameters(params map[string]string) []any {
	var keys []string
	for key := range params {
		if strings.HasPrefix(key, paramWorkflowParameterPrefix) {
			keys = append(keys, key)
		}
	}
	sortStrings(keys)
	parameters := make([]any, 0, len(keys))
	for _, key := range keys {
		parameters = append(parameters, map[string]any{
			"name":  strings.TrimPrefix(key, paramWorkflowParameterPrefix),
			"value": params[key],
		})
	}
	return parameters
}

func (r Runner) agentShutdownHandoff(action executor.Action) (executor.ActionOutcome, error) {
	if len(action.Group.NodeReleases) == 0 {
		err := fmt.Errorf("AgentShutdown requires at least one selected node release")
		return blocked(err), err
	}
	return executor.ActionOutcome{
		Outcome: executor.OutcomeSucceeded,
		Details: map[string]any{
			"handoff":      "NodeReleaseAuditSignal",
			"nodeReleases": len(action.Group.NodeReleases),
		},
	}, nil
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

func (r Runner) now() time.Time {
	if r.Clock != nil {
		return r.Clock().UTC()
	}
	return time.Now().UTC()
}

func dnsPrefix(value string) string {
	value = strings.ToLower(value)
	var b strings.Builder
	lastHyphen := false
	for _, r := range value {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if valid {
			b.WriteRune(r)
			lastHyphen = false
			continue
		}
		if !lastHyphen {
			b.WriteByte('-')
			lastHyphen = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "shutdown-workflow"
	}
	if len(out) > 48 {
		out = strings.TrimRight(out[:48], "-")
	}
	return out + "-"
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
