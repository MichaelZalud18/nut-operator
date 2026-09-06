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
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	executorpkg "github.com/MichaelZalud18/nut-operator/internal/executor"
	"github.com/MichaelZalud18/nut-operator/internal/kubeactions"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
)

// F-110: internal/kubeactions/apistall_fault_injection_test.go proves the runner itself returns
// once its caller's context expires. What that does not prove is that a real compiled flow ever
// builds that caller's context from anything -- the executor derives it from the group's own
// declared Timeout (EX-11's groupCtx, executor.go), not from whatever context Execute happened
// to receive. This test drives the real chain end to end: a plan through planner.Compile, a real
// kubeactions.Runner backed by a fake Kubernetes client whose Get never returns, and a real
// Executor.Execute -- and confirms a wedged apiserver during a live shutdown produces a bounded,
// promptly-reported abort rather than a flow that hangs for as long as the cluster stays down.
func TestExecutorAbortsPromptlyWhenTheKubernetesAPIStallsDuringAnAction(t *testing.T) {
	declared := map[string]time.Duration{
		// Short enough that the test finishes in well under a second; long enough that a false
		// pass from an interceptor that never actually blocks would be implausible.
		"applications": 150 * time.Millisecond,
		"databases":    30 * time.Second,
		"storage":      30 * time.Second,
	}
	plan, waves, groups := simulatedTieredPlan(t, declared, planner.HistoryInputs{})

	// simulatedTieredPlan builds groups shaped for the adaptive-boundary specs, which never
	// actuate anything (they all run in dry-run). Point "applications" -- the group in wave 0 --
	// at a real scale target so this run actually reaches kubeactions.Runner.RunAction.
	for i := range groups {
		if groups[i].Name != "applications" {
			continue
		}
		groups[i].Params = map[string]string{"replicas": "0"}
		groups[i].SelectedTargets = []executorpkg.Target{
			{APIVersion: "apps/v1", Kind: "Deployment", Namespace: "apps", Name: "web"},
		}
	}

	scheme := runtime.NewScheme()
	if err := appsv1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}
	if err := powerv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme returned error: %v", err)
	}
	replicas := int32(3)
	var apiCallsObserved int
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&appsv1.Deployment{
			ObjectMeta: metav1.ObjectMeta{Namespace: "apps", Name: "web"},
			Spec:       appsv1.DeploymentSpec{Replicas: &replicas},
		},
	).WithInterceptorFuncs(interceptor.Funcs{
		Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			if key.Name != "web" {
				return c.Get(ctx, key, obj, opts...)
			}
			apiCallsObserved++
			// A wedged apiserver: no response until the caller gives up. If this is ever
			// reached, the executor's own group timeout is what has to end it -- there is no
			// external deadline in this test to fall back on.
			<-ctx.Done()
			return ctx.Err()
		},
	}).Build()

	runner := kubeactions.Runner{Client: kube}
	writer := &fakeAuditStore{}
	input := executorpkg.Input{
		ExecutionID: "sim-apistall", ShutdownFlow: "sim", PlanConfigHash: plan.Hash,
		Mode: executorpkg.ModeEnforce, Approved: true,
		Waves: waves, Groups: groups,
		Adaptive: executorpkg.AdaptiveInput{Observation: runtimeObservation(true, false, 1000), FinalTier: 1},
	}

	started := time.Now()
	// context.Background(): deliberately no timeout supplied by the caller, so the only thing
	// that can end this run is the executor's own EX-11 enforcement of the group's declared
	// Timeout.
	result, err := (executorpkg.Executor{
		Writer: writer, Runner: runner, Clock: time.Now, NewID: func() string { return "sim-apistall" },
	}).Execute(context.Background(), input)
	elapsed := time.Since(started)

	if apiCallsObserved == 0 {
		t.Fatal("fixture did not exercise the stalled Get -- the interceptor never observed a call for the target Deployment")
	}
	// recordAborted (executor.go) returns both a non-nil error (the failure that caused the
	// abort, surfaced to the caller) and a Result carrying Phase: Aborted (the audited outcome)
	// -- an aborted flow is reported both ways, not one or the other.
	if err == nil {
		t.Fatal("expected Execute to return the stall failure as an error alongside the aborted result")
	}
	if !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Fatalf("expected the returned error to attribute the abort to the stalled call, got %v", err)
	}
	if result.Phase != executorpkg.PhaseAborted {
		t.Fatalf("phase = %q, want Aborted -- a stalled Kubernetes call should fail its group and abort the flow, not hang or silently succeed", result.Phase)
	}

	// The bound this test exists to prove: Execute returned once the 150ms group Timeout
	// expired, not after the interceptor's stall (which has no timer of its own -- a regression
	// here would hang the whole test process, not just run slowly) and not after the 30s+30s
	// declared timeouts of the waves the flow never reached.
	if elapsed > 5*time.Second {
		t.Fatalf("Execute took %s against a 150ms group timeout -- a wedged apiserver call is not being bounded by the group's declared Timeout", elapsed)
	}

	// EX-11's own vocabulary for this: the group itself is recorded Failed with a TimedOut
	// outcome (distinct from the flow-level Aborted phase asserted above), which is what lets
	// an operator reading the audit trail tell "this group's declared timeout expired" apart
	// from every other way a group can fail.
	foundTimedOutGroup := false
	for _, group := range writer.executionGroups {
		if group.GroupName == "applications" && group.Phase == executorpkg.PhaseFailed && group.Details["outcome"] == executorpkg.OutcomeTimedOut {
			foundTimedOutGroup = true
		}
	}
	if !foundTimedOutGroup {
		t.Fatalf("expected the audit trail to carry a Failed/TimedOut record for the applications group, got %#v", writer.executionGroups)
	}

	foundTimeoutAttempt := false
	for _, attempt := range writer.actionAttempts {
		if attempt.GroupName == "applications" && strings.Contains(attempt.Error, context.DeadlineExceeded.Error()) {
			foundTimeoutAttempt = true
		}
	}
	if !foundTimeoutAttempt {
		t.Fatalf("expected an action attempt recording the stalled Get as a deadline-exceeded failure, got %#v", writer.actionAttempts)
	}

	// Bounded blast radius: the databases and storage waves declared 30s timeouts each, so if
	// the flow had kept going after the stall instead of aborting, this run would still be
	// well under this test's own patience budget -- but it must not have run them at all, since
	// the plan never reached wave 1 or 2.
	for _, group := range writer.executionGroups {
		if group.GroupName == "databases" || group.GroupName == "storage" {
			t.Fatalf("group %q was recorded, but the flow should have aborted at wave 0 before reaching it: %#v", group.GroupName, group)
		}
	}

}
