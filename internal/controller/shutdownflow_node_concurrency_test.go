package controller

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/executor"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestAsyncNodeClaims(t *testing.T) {
	for _, overlap := range []bool{false, true} {
		t.Run(map[bool]string{false: "disjoint", true: "shared"}[overlap], func(t *testing.T) {
			r, _, _, _ := asyncFlowFixture(t)
			a, b := createAsyncFlow(t, r, "a"), createAsyncFlow(t, r, "b")
			for _, flow := range []*power.ShutdownFlow{a, b} {
				node := flow.Name
				objects := []client.Object{
					&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: node, Labels: map[string]string{"owner": node}}},
					&power.PowerInventoryNode{ObjectMeta: metav1.ObjectMeta{Name: "inventory-" + node}, Spec: power.PowerInventoryNodeSpec{NodeName: node, CommunicationPathExempt: boolPtr(true)}},
					&power.PowerInventoryEdge{ObjectMeta: metav1.ObjectMeta{Name: "feed-" + node}, Spec: power.PowerInventoryEdgeSpec{From: power.PowerInventoryEntityReference{Kind: power.PowerInventoryEntityUPSDevice, Name: "ups"}, To: power.PowerInventoryEntityReference{Kind: power.PowerInventoryEntityNode, Name: node}, Relation: power.PowerInventoryEdgeFeeds, Input: "psu-a"}},
				}
				for _, obj := range objects {
					if err := r.Create(context.Background(), obj); err != nil {
						t.Fatal(err)
					}
				}
				if overlap {
					node = a.Name
				}
				for i := range flow.Spec.Groups {
					flow.Spec.Groups[i].Action = power.ShutdownStepCordonNodes
					flow.Spec.Groups[i].Target.NodeSelector = &metav1.LabelSelector{MatchLabels: map[string]string{"owner": node}}
				}
				if err := r.Update(context.Background(), flow); err != nil {
					t.Fatal(err)
				}
			}
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(release) }) })
			var otherCalls atomic.Int32
			r.ExecutorRunner = storageTestRunner(func(ctx context.Context, action executor.Action) (executor.ActionOutcome, error) {
				if action.ShutdownFlow == b.Name {
					otherCalls.Add(1)
				}
				if action.ShutdownFlow == a.Name && action.Group.Name == "first" {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return executor.ActionOutcome{}, ctx.Err()
					}
				}
				return executor.ActionOutcome{Outcome: executor.OutcomeSucceeded}, nil
			})
			reconcileAsync(t, r, a)
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatalf("first action did not start: %+v", r.runs.snapshot(a.Name).flow.Status)
			}
			reconcileAsync(t, r, b)
			awaitRunDone(t, r, b.Name)
			current := reconcileAsync(t, r, b)
			if overlap {
				condition := meta.FindStatusCondition(current.Status.Conditions, power.ConditionExecutionReady)
				if condition == nil || condition.Reason != "ExecutionConflict" || otherCalls.Load() != 0 {
					t.Fatalf("shared node executed concurrently: %+v", current.Status)
				}
			} else if current.Status.LastExecution == nil || current.Status.LastExecution.Phase != power.ShutdownExecutionPhaseCompleted || otherCalls.Load() != 2 {
				t.Fatalf("disjoint node flow starved: %+v", current.Status)
			}
			once.Do(func() { close(release) })
			awaitRunDone(t, r, a.Name)
		})
	}
}
