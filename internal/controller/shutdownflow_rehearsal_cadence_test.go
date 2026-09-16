package controller

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/executor"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestAsyncRehearsalUsesActivePublishCadence(t *testing.T) {
	for _, tc := range []struct {
		name          string
		cadence       *power.PublishCadenceSpec
		idle          time.Duration
		active        time.Duration
		deleteCluster bool
	}{
		{name: "defaults", idle: time.Minute, active: 10 * time.Second},
		{name: "configured", idle: 45 * time.Second, active: 7 * time.Second, cadence: &power.PublishCadenceSpec{
			Idle: &metav1.Duration{Duration: 45 * time.Second}, Active: &metav1.Duration{Duration: 7 * time.Second},
		}},
		{name: "missing PMC while blocked", idle: 45 * time.Second, active: 7 * time.Second, deleteCluster: true, cadence: &power.PublishCadenceSpec{
			Idle: &metav1.Duration{Duration: 45 * time.Second}, Active: &metav1.Duration{Duration: 7 * time.Second},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, _, _, _ := asyncFlowFixture(t)
			var clock atomic.Int64
			clock.Store(time.Now().UnixNano())
			r.Clock = func() time.Time { return time.Unix(0, clock.Load()) }
			ctx := context.Background()
			cluster := &power.PowerManagementCluster{}
			if err := r.Get(ctx, client.ObjectKey{Name: "management"}, cluster); err != nil {
				t.Fatal(err)
			}
			cluster.Spec.Observability.PublishCadence = tc.cadence
			if err := r.Update(ctx, cluster); err != nil {
				t.Fatal(err)
			}
			flow := createAsyncFlow(t, r, "rehearsal")
			flow.Spec.Triggers = []power.ShutdownTrigger{{Type: power.ShutdownTriggerLowBattery}}
			flow.Annotations[power.ShutdownFlowRehearsalRequestAnnotation] = "cadence-test"
			if err := r.Update(ctx, flow); err != nil {
				t.Fatal(err)
			}
			blocked, release := make(chan struct{}), make(chan struct{})
			t.Cleanup(func() { close(release) })
			r.ExecutorRunner = storageTestRunner(func(ctx context.Context, action executor.Action) (executor.ActionOutcome, error) {
				if action.Group.Name == "first" {
					close(blocked)
					select {
					case <-release:
					case <-ctx.Done():
						return executor.ActionOutcome{}, ctx.Err()
					}
				}
				return executor.ActionOutcome{Outcome: executor.OutcomeSucceeded}, nil
			})
			req := ctrl.Request{NamespacedName: client.ObjectKeyFromObject(flow)}
			result, err := r.Reconcile(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if result.RequeueAfter != tc.idle {
				t.Fatalf("initial cadence = %s, want idle %s", result.RequeueAfter, tc.idle)
			}
			select {
			case <-blocked:
			case <-time.After(3 * time.Second):
				t.Fatal("rehearsal did not enter blocked action")
			}
			var lastPublish time.Time
			for i := range 3 {
				if tc.deleteCluster && i == 1 {
					if err := r.Delete(ctx, cluster); err != nil {
						t.Fatal(err)
					}
				}
				clock.Add(int64(time.Second))
				result, err = r.Reconcile(ctx, req)
				if err != nil {
					t.Fatal(err)
				}
				wantCadence := tc.active
				if tc.deleteCluster && i >= 1 {
					wantCadence = 10 * time.Second
				}
				if result.RequeueAfter != wantCadence {
					t.Fatalf("running cadence = %s, want active %s", result.RequeueAfter, wantCadence)
				}
				current := &power.ShutdownFlow{}
				if err := r.Get(ctx, req.NamespacedName, current); err != nil {
					t.Fatal(err)
				}
				if current.Status.LastPublishTime == nil || !current.Status.LastPublishTime.After(lastPublish) {
					t.Fatal("heartbeat did not advance during blocked action")
				}
				lastPublish = current.Status.LastPublishTime.Time
				if current.Status.TriggerEvaluation == nil || current.Status.TriggerEvaluation.Eligible {
					t.Fatalf("expected ineligible trigger: %+v", current.Status.TriggerEvaluation)
				}
				for _, decision := range current.Status.TriggerEvaluation.Decisions {
					if decision.Matched {
						t.Fatalf("unexpected matched trigger: %+v", decision)
					}
				}
				if execution := current.Status.LastExecution; execution == nil || !execution.Rehearsal || execution.Phase != power.ShutdownExecutionPhaseRunning {
					t.Fatalf("expected running rehearsal: %+v", execution)
				}
			}
		})
	}
}
