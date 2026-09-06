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

package kubeactions

import (
	"context"
	"errors"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/executor"
)

// F-110: every other spec in this package controls what the Kubernetes API *returns* --
// apierrors, disruption-budget denials, self-exclusion. None of them control how long it takes
// to return, which is a different failure mode: a wedged apiserver, a stuck admission webhook,
// or a slow etcd don't answer with an error, they just don't answer. The executor's own bound
// on that (executor.go's groupCtx, derived from the group's declared Timeout) is what is
// supposed to prevent an unresponsive cluster from hanging a shutdown flow forever. This file
// proves the runner surfaces that bound rather than blocking through it.
//
// The interceptor functions below select on ctx.Done() instead of a bare time.Sleep. A bare
// sleep would prove only that *this test* eventually returns; selecting on the context is what
// proves the client call is actually context-aware the way the real client-go REST client is,
// so the runner returning promptly here also predicts what happens against a real clientset.

// stallGetFor returns an interceptor Get that blocks until either ctx is done or a generous
// upper bound elapses, simulating an apiserver that is not answering rather than one that
// answers with an error. The upper bound exists only so a bug that made the runner stop
// respecting ctx fails this test in seconds instead of hanging the suite.
func stallGetFor(objName string, blocked *int) interceptor.Funcs {
	return interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if key.Name != objName {
				return c.Get(ctx, key, obj, opts...)
			}
			*blocked++
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
				return errors.New("stall interceptor: context was never cancelled")
			}
		},
	}
}

// TestRunnerScaleWorkloadRespectsCallerTimeoutAgainstAStalledGet proves the bound the executor
// depends on rather than assumes it: ScaleWorkload's Get on the target Deployment is where the
// real production code (runner.go's scaleDeployment) makes its first Kubernetes call, so this
// stalls exactly that call and confirms RunAction returns once the caller's context expires --
// the same context.WithTimeout(actionCtx, effectiveTimeout) the executor builds from the
// group's own declared Timeout -- rather than blocking until the interceptor's own upper bound.
func TestRunnerScaleWorkloadRespectsCallerTimeoutAgainstAStalledGet(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}
	if err := powerv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}
	replicas := int32(3)
	var blocked int
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&appsv1.Deployment{ObjectMeta: objectMeta("apps", "web"), Spec: appsv1.DeploymentSpec{Replicas: &replicas}},
	).WithInterceptorFuncs(stallGetFor("web", &blocked)).Build()
	runner := Runner{Client: kube}

	// This mirrors what executor.go actually builds for a group's declared Timeout, rather than
	// hand-picking an arbitrary short duration: a real shutdown group with a 100ms budget stalled
	// against a wedged apiserver.
	const groupTimeout = 100 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), groupTimeout)
	defer cancel()

	started := time.Now()
	outcome, err := runner.RunAction(ctx, executor.Action{
		ExecutionID: "e1", ShutdownFlow: "f1", PlanConfigHash: "h1",
		Group: executor.Group{
			Name:   "applications",
			Action: ActionScale,
			Params: map[string]string{paramReplicas: "0"},
			SelectedTargets: []executor.Target{
				{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "apps", Name: "web"},
			},
		},
	})
	elapsed := time.Since(started)

	if blocked == 0 {
		t.Fatal("fixture did not exercise the stalled Get -- the interceptor never observed a call for the target Deployment")
	}
	if err == nil {
		t.Fatalf("expected the stalled Get to surface as an error once the group timeout expired, got outcome %#v", outcome)
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected a context.DeadlineExceeded error, got %v", err)
	}
	// Loose bound: this asserts the runner returned when the 100ms group context expired, not
	// when the interceptor's 5s upper bound did. A regression that dropped ctx propagation
	// through to the client call would make this take ~5s instead.
	if elapsed > 2*time.Second {
		t.Fatalf("RunAction took %s against a %s group timeout -- it did not return when the context expired", elapsed, groupTimeout)
	}
}

// TestRunnerDrainNodesRespectsCallerTimeoutAgainstAStalledList is the same proof against
// DrainNodes' pod listing (evictPodsOnNode), the other real production path that lists cluster
// state before acting on it.
func TestRunnerDrainNodesRespectsCallerTimeoutAgainstAStalledList(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}
	if err := powerv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}
	var blocked int
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "worker-a"}},
	).WithInterceptorFuncs(interceptor.Funcs{
		List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
			blocked++
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
				return errors.New("stall interceptor: context was never cancelled")
			}
		},
	}).Build()
	runner := Runner{Client: kube}

	const groupTimeout = 100 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), groupTimeout)
	defer cancel()

	started := time.Now()
	_, err := runner.RunAction(ctx, executor.Action{
		ExecutionID: "e1", ShutdownFlow: "f1", PlanConfigHash: "h1",
		Group: executor.Group{
			Name:   "drain-workers",
			Action: ActionDrainNodes,
			SelectedTargets: []executor.Target{
				{Kind: "Node", Name: "worker-a"},
			},
		},
	})
	elapsed := time.Since(started)

	if blocked == 0 {
		t.Fatal("fixture did not exercise the stalled List")
	}
	if err == nil {
		t.Fatal("expected the stalled List to surface as an error once the group timeout expired")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected a context.DeadlineExceeded error, got %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("RunAction took %s against a %s group timeout -- it did not return when the context expired", elapsed, groupTimeout)
	}
}
