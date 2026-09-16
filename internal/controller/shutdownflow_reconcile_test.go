package controller

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	ctrl "sigs.k8s.io/controller-runtime"
)

// Existing policy assertions inspect settled status. Exercise the same owned
// execution path as the manager, then explicitly publish its completed snapshot.
func (r *ShutdownFlowReconciler) reconcileForTest(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	if r.runs == nil {
		r.runs = newFlowRuns(maxConcurrentFlowRuns)
		runCtx, cancel := context.WithCancel(ctx)
		stopped := make(chan struct{})
		go func() { _ = r.runs.Start(runCtx); close(stopped) }()
		<-r.runs.ready
		DeferCleanup(func() { cancel(); <-stopped })
	}
	result, err := r.Reconcile(ctx, req)
	if err != nil {
		return result, err
	}
	timeout := time.NewTimer(10 * time.Second)
	defer timeout.Stop()
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()
	for {
		run := r.runs.snapshot(req.Name)
		if run == nil {
			return result, nil
		}
		if run.done {
			_, err := r.Reconcile(ctx, req)
			return result, err
		}
		select {
		case <-ctx.Done():
			return result, ctx.Err()
		case <-timeout.C:
			return result, fmt.Errorf("timed out awaiting owned flow execution")
		case <-tick.C:
		}
	}
}
