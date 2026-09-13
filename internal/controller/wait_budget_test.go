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
	"sync/atomic"
	"testing"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/executor"
	"github.com/MichaelZalud18/nut-operator/internal/resolver"
	"github.com/MichaelZalud18/nut-operator/internal/shutdownflow"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestWaitBudgetThroughProductionAdapters(t *testing.T) {
	for _, linear := range []bool{false, true} {
		for _, dryRun := range []bool{false, true} {
			for _, tc := range []struct {
				name                             string
				duration, timeout, budget, sleep time.Duration
				runtime                          int64
				expires                          bool
			}{
				{name: "unbounded hour", duration: time.Hour, budget: time.Hour, sleep: time.Hour, runtime: 10000},
				{name: "longer timeout", duration: time.Minute, timeout: time.Hour, budget: time.Minute, sleep: time.Minute, runtime: 10000},
				{name: "compressed", duration: 100 * time.Second, timeout: 200 * time.Second, budget: 100 * time.Second, sleep: 40 * time.Second, runtime: 50},
				{name: "deadline", duration: time.Hour, timeout: 10 * time.Millisecond, budget: 10 * time.Millisecond, sleep: time.Hour, runtime: 10000, expires: true},
			} {
				t.Run(fmt.Sprintf("linear=%v/dryRun=%v/%s", linear, dryRun, tc.name), func(t *testing.T) {
					flow := &power.ShutdownFlow{ObjectMeta: metav1.ObjectMeta{Name: "wait-budget"}}
					flow.Spec.Triggers = []power.ShutdownTrigger{{Type: power.ShutdownTriggerOnBattery}}
					var timeout *metav1.Duration
					if tc.timeout > 0 {
						timeout = &metav1.Duration{Duration: tc.timeout}
					}
					if linear {
						flow.Spec.Steps = []power.ShutdownStep{{ID: "pause", Type: power.ShutdownStepWait, Duration: &metav1.Duration{Duration: tc.duration}, Timeout: timeout}}
					} else {
						flow.Spec.Groups = []power.ShutdownGroup{{Name: "pause", Action: power.ShutdownStepWait, Params: map[string]string{"duration": tc.duration.String()}, Timeout: timeout}}
					}
					compiled := shutdownflow.CompileFlow(flow, resolver.StructuralBundle{}, power.PowerShutdownTierPolicySpec{})
					if compiled.ConfigHash == "" || durationOrZero(compiled.EstimatedDuration) != tc.budget {
						t.Fatalf("wrong compiled budget: %#v, diagnostics %#v", compiled.EstimatedDuration, compiled.Diagnostics)
					}
					if !linear && compiled.GroupEstimates[0].Declared.Duration != tc.budget {
						t.Fatal("history fallback lost Wait duration")
					}
					waves := executorWavesFromFlow(compiled.Waves, compiled.Steps)
					if len(waves) != 1 || waves[0].Duration != tc.budget {
						t.Fatalf("wave budget lost: %#v", waves)
					}
					scheme := runtime.NewScheme()
					if err := power.AddToScheme(scheme); err != nil {
						t.Fatal(err)
					}
					r := ShutdownFlowReconciler{Client: fake.NewClientBuilder().WithScheme(scheme).Build()}
					groups, err := r.executorGroupsFromFlow(context.Background(), flow)
					if err != nil {
						t.Fatal(err)
					}
					var now atomic.Int64
					now.Store(time.Now().UnixNano())
					writer := &fakeAuditStore{}
					e := executor.Executor{Writer: writer, Runner: succeedingControllerActionRunner{}, Clock: func() time.Time { return time.Unix(0, now.Load()) }, NewID: func() string { return "wait-budget" }}
					e.Sleep = func(ctx context.Context, duration time.Duration) error {
						if duration != tc.sleep {
							t.Errorf("sleep=%s, want %s", duration, tc.sleep)
						}
						deadline, bounded := ctx.Deadline()
						if bounded != (tc.timeout > 0) {
							t.Errorf("unexpected deadline presence: %v", bounded)
						}
						if tc.name == "compressed" && (time.Until(deadline) > 80*time.Second || time.Until(deadline) < 75*time.Second) {
							t.Error("timeout was not compressed with Wait")
						}
						if tc.expires {
							<-ctx.Done()
							return ctx.Err()
						}
						now.Add(int64(duration))
						return nil
					}
					mode := executor.ModeEnforce
					if dryRun {
						mode = executor.ModeDryRun
					}
					ctx, cancel := context.WithCancel(context.Background())
					defer cancel()
					result, err := e.Execute(ctx, executor.Input{ShutdownFlow: flow.Name, PlanConfigHash: compiled.ConfigHash, Mode: mode, Approved: true, Waves: waves, Groups: groups, Adaptive: executor.AdaptiveInput{Observation: runtimeObservation(true, false, tc.runtime)}})
					if tc.expires {
						if err == nil || result.Phase != executor.PhaseAborted {
							t.Fatalf("expired Wait succeeded: %#v, %v", result, err)
						}
						if len(writer.actionAttempts) != 1 || writer.actionAttempts[0].Outcome != executor.OutcomeTimedOut {
							t.Fatalf("timeout audit outcome incorrect: %#v", writer.actionAttempts)
						}
					} else if err != nil {
						t.Fatal(err)
					}
					if result.DryRun != dryRun {
						t.Fatalf("mode mismatch: dryRun=%v, want %v", result.DryRun, dryRun)
					}
				})
			}
		}
	}
}
