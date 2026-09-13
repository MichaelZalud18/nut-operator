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
	"errors"
	"testing"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/executor"
	"github.com/MichaelZalud18/nut-operator/internal/planner"
)

func TestCommunicationBarrierWaitsForOverlappedWork(t *testing.T) {
	for _, outcome := range []string{"success", "failure", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			started := make(chan struct{})
			earlierStarted := make(chan struct{})
			unrelated := make(chan struct{})
			releaseWork := make(chan struct{})
			carrier := make(chan struct{})
			done := make(chan error, 1)
			high, middle, low := int32(4), int32(3), int32(2)
			earliest := int32(5)
			waves := []executor.Wave{
				{Index: 0, ShutdownTier: &earliest, Duration: time.Nanosecond, Groups: []string{"earlier"}},
				{Index: 1, ShutdownTier: &high, Duration: time.Nanosecond, Groups: []string{"work"}},
				{Index: 2, ShutdownTier: &middle, Groups: []string{"unrelated"}},
				{Index: 3, ShutdownTier: &low, Groups: []string{"release"}},
			}
			applyCommunicationBarriers(waves, &power.PublishedPlannerArtifactStatus{Graph: power.PlannerGraphStatus{Edges: []power.PlannerGraphEdgeStatus{{From: "work", To: "release", Relation: planner.GraphEdgeRelationCommunicationPath}}}})
			if !waves[3].CommunicationBarrier || waves[2].CommunicationBarrier {
				t.Fatal("incorrect barrier mapping")
			}
			e := executor.Executor{NewID: func() string { return "barrier" },
				Sleep: func(ctx context.Context, d time.Duration) error {
					switch d {
					case 2 * time.Hour:
						close(earlierStarted)
						select {
						case <-releaseWork:
						case <-ctx.Done():
							return ctx.Err()
						}
					case time.Hour:
						close(started)
						select {
						case <-releaseWork:
						case <-ctx.Done():
							return ctx.Err()
						}
						if outcome == "failure" {
							return errors.New("dependent failed")
						}
					case time.Second:
						close(unrelated)
					default:
						close(carrier)
					}
					return nil
				},
			}
			go func() {
				_, err := e.Execute(ctx, executor.Input{ShutdownFlow: "communication-test", PlanConfigHash: "plan", Mode: executor.ModeDryRun, TierOverrunPolicy: executor.TierOverrunPolicyOverlap, Waves: waves,
					Groups: []executor.Group{{Name: "earlier", Action: executor.ActionWait, WaitDuration: 2 * time.Hour}, {Name: "work", Action: executor.ActionWait, WaitDuration: time.Hour}, {Name: "unrelated", Action: executor.ActionWait, WaitDuration: time.Second}, {Name: "release", Action: executor.ActionWait, WaitDuration: time.Millisecond}},
				})
				done <- err
			}()
			for _, signal := range []<-chan struct{}{earlierStarted, started, unrelated} {
				select {
				case <-signal:
				case <-ctx.Done():
					t.Fatal("overlap did not start")
				}
			}
			select {
			case <-carrier:
				t.Fatal("carrier released while dependent still running")
			case <-time.After(25 * time.Millisecond):
			}
			if outcome == "cancel" {
				cancel()
			} else {
				close(releaseWork)
			}
			select {
			case err := <-done:
				if (err != nil) != (outcome != "success") {
					t.Fatalf("outcome %s: %v", outcome, err)
				}
			case <-time.After(time.Second):
				t.Fatal("barrier did not finish after dependent stopped")
			}
			select {
			case <-carrier:
				if outcome != "success" {
					t.Fatal("carrier released after dependent failed")
				}
			default:
				if outcome == "success" {
					t.Fatal("carrier never released")
				}
			}
		})
	}
}
