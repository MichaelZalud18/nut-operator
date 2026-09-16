package controller

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/audit"
	"github.com/MichaelZalud18/nut-operator/internal/executor"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

type ownedAuditStore struct {
	audit.NoopStore
	closed     atomic.Bool
	executions atomic.Int32
}

func (s *ownedAuditStore) Close() error { s.closed.Store(true); return nil }
func (s *ownedAuditStore) RecordShutdownFlowExecution(_ context.Context, record audit.ShutdownFlowExecution) error {
	if s.closed.Load() {
		return errors.New("execution wrote to closed store")
	}
	s.executions.Add(1)
	return nil
}

type ownedAuditConnector struct {
	mu     sync.Mutex
	stores []*ownedAuditStore
}

func (c *ownedAuditConnector) OpenAuditStore(context.Context, *power.PowerManagementCluster) (audit.Store, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := &ownedAuditStore{}
	c.stores = append(c.stores, s)
	return s, nil
}

func asyncFlowFixture(t *testing.T) (*ShutdownFlowReconciler, *ownedAuditConnector, context.CancelFunc, <-chan struct{}) {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := power.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	now := metav1.Now()
	ups := upsDeviceTelemetry("ups", power.UPSDevicePhaseOnBattery, 3600, 90, 10)
	ups.Spec.Driver, ups.Spec.PowerDomains = "snmp-ups", []string{"rack"}
	ups.Spec.Endpoint = &power.UPSEndpointSpec{Host: "ups.example.net"}
	ups.Status.LastPollTime = &now
	profile := &power.UPSCapabilityProfile{ObjectMeta: metav1.ObjectMeta{Name: "universal"}, Spec: power.UPSCapabilityProfileSpec{Version: "1.0.0", Selector: power.UPSCapabilityProfileSelector{Universal: boolPtr(true)}, Telemetry: power.UPSCapabilityTelemetrySpec{Variables: []string{"ups.status", "battery.runtime"}}}}
	cluster := &power.PowerManagementCluster{ObjectMeta: metav1.ObjectMeta{Name: "management"}, Status: power.PowerManagementClusterStatus{Storage: power.StorageStatus{Ready: true, Mode: power.PowerStorageExternalPostgres}}}
	c := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(&power.ShutdownFlow{}, &power.UPSDevice{}, &power.PowerManagementCluster{}).WithObjects(&ups, profile, cluster).Build()
	connector := &ownedAuditConnector{}
	r := &ShutdownFlowReconciler{Client: c, APIReader: c, Scheme: scheme, StorageConnector: connector, runs: newFlowRuns(maxConcurrentFlowRuns)}
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { _ = r.runs.Start(ctx); close(stopped) }()
	<-r.runs.ready
	t.Cleanup(func() { cancel(); <-stopped })
	return r, connector, cancel, stopped
}

func createAsyncFlow(t *testing.T, r *ShutdownFlowReconciler, name string) *power.ShutdownFlow {
	t.Helper()
	flow := &power.ShutdownFlow{ObjectMeta: metav1.ObjectMeta{Name: name, UID: types.UID(name), Generation: 1}, Spec: power.ShutdownFlowSpec{
		Mode:                 power.ShutdownFlowModeEnforce,
		ManagementClusterRef: &power.ObjectNameReference{Name: "management"},
		CommunicationPaths:   []power.FlowCommunicationPath{{Service: "OperatorAPI", Exempt: true}, {Service: "NUT", Exempt: true}},
		Triggers:             []power.ShutdownTrigger{{Type: power.ShutdownTriggerOnBattery}},
		Groups:               []power.ShutdownGroup{{Name: "first", Action: power.ShutdownStepNotify, Before: []string{"second"}}, {Name: "second", Action: power.ShutdownStepNotify}},
	}}
	flow.Spec.Safety.ApprovalAnnotation = "test/approval"
	flow.Spec.Safety.AllowUnidentifiedDevices = boolPtr(true)
	flow.Annotations = map[string]string{"test/approval": "true"}
	if err := r.Create(context.Background(), flow); err != nil {
		t.Fatal(err)
	}
	return flow
}

func reconcileAsync(t *testing.T, r *ShutdownFlowReconciler, flow *power.ShutdownFlow) *power.ShutdownFlow {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := r.Reconcile(ctx, ctrl.Request{NamespacedName: client.ObjectKeyFromObject(flow)}); err != nil {
		t.Fatal(err)
	}
	current := &power.ShutdownFlow{}
	if err := r.Get(ctx, client.ObjectKeyFromObject(flow), current); err != nil {
		t.Fatal(err)
	}
	return current
}

func awaitRunDone(t *testing.T, r *ShutdownFlowReconciler, name string) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for {
		if run := r.runs.snapshot(name); run == nil || run.done {
			return
		}
		select {
		case <-r.runs.events:
		case <-deadline.C:
			t.Fatalf("execution %s did not finish: %+v", name, r.runs.snapshot(name))
		}
	}
}

func TestAsyncFlowProgressAndIndependence(t *testing.T) {
	r, connector, _, _ := asyncFlowFixture(t)
	var clock atomic.Int64
	clock.Store(time.Now().UnixNano())
	r.Clock = func() time.Time { return time.Unix(0, clock.Load()) }
	a := createAsyncFlow(t, r, "blocked")
	b := createAsyncFlow(t, r, "independent")
	blocked := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	var blockedOnce sync.Once
	r.ExecutorRunner = storageTestRunner(func(ctx context.Context, action executor.Action) (executor.ActionOutcome, error) {
		if action.ShutdownFlow == a.Name {
			calls.Add(1)
			if action.Group.Name == "second" {
				blockedOnce.Do(func() { close(blocked) })
				select {
				case <-release:
				case <-ctx.Done():
					return executor.ActionOutcome{}, ctx.Err()
				}
			}
		}
		return executor.ActionOutcome{Outcome: executor.OutcomeSucceeded}, nil
	})
	reconcileAsync(t, r, a)
	select {
	case <-blocked:
	case <-time.After(3 * time.Second):
		t.Fatalf("flow never entered action: %+v", r.runs.snapshot(a.Name).flow.Status)
	}
	var lastPublish time.Time
	for range 3 {
		clock.Add(int64(2 * time.Second))
		current := reconcileAsync(t, r, a)
		if current.Status.LastExecution == nil || current.Status.LastExecution.Phase != power.ShutdownExecutionPhaseRunning || current.Status.LastExecution.GroupCount != 1 || current.Status.LastPublishTime == nil {
			t.Fatalf("missing live progress: %+v", current.Status)
		}
		if !current.Status.LastPublishTime.After(lastPublish) {
			t.Fatal("heartbeat did not advance during blocked action")
		}
		lastPublish = current.Status.LastPublishTime.Time
	}
	if calls.Load() != 2 {
		t.Fatalf("duplicate execution: %d calls", calls.Load())
	}
	reconcileAsync(t, r, b)
	awaitRunDone(t, r, b.Name)
	if current := reconcileAsync(t, r, b); current.Status.LastExecution == nil || current.Status.LastExecution.Phase != power.ShutdownExecutionPhaseCompleted {
		t.Fatalf("independent flow did not finish: %+v", current.Status)
	}
	connector.mu.Lock()
	openExecution := false
	for _, store := range connector.stores {
		if store.executions.Load() == 1 && !store.closed.Load() {
			openExecution = true
		}
	}
	connector.mu.Unlock()
	if !openExecution {
		t.Fatal("blocked execution lost its audit store")
	}
	close(release)
	awaitRunDone(t, r, a.Name)
	current := reconcileAsync(t, r, a)
	if current.Status.LastExecution.Phase != power.ShutdownExecutionPhaseCompleted || current.Status.LastExecution.GroupCount != 2 {
		t.Fatalf("completion: %+v", current.Status.LastExecution)
	}
	reconcileAsync(t, r, a)
	awaitRunDone(t, r, a.Name)
	reconcileAsync(t, r, a)
	if calls.Load() != 2 {
		t.Fatal("completed episode ran again")
	}
	connector.mu.Lock()
	defer connector.mu.Unlock()
	for _, store := range connector.stores {
		if !store.closed.Load() {
			t.Fatal("audit store leaked")
		}
	}
}

func TestAsyncFlowCancellation(t *testing.T) {
	for _, reason := range []string{"manager", "delete", "spec", "replacement"} {
		t.Run(reason, func(t *testing.T) {
			r, _, cancel, stopped := asyncFlowFixture(t)
			flow := createAsyncFlow(t, r, "cancel")
			entered, canceled, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var once sync.Once
			t.Cleanup(func() { once.Do(func() { close(finish) }) })
			var calls atomic.Int32
			r.ExecutorRunner = storageTestRunner(func(ctx context.Context, _ executor.Action) (executor.ActionOutcome, error) {
				if calls.Add(1) == 1 {
					close(entered)
					<-ctx.Done()
					close(canceled)
					<-finish
					return executor.ActionOutcome{}, ctx.Err()
				}
				return executor.ActionOutcome{Outcome: executor.OutcomeSucceeded}, nil
			})
			reconcileAsync(t, r, flow)
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatalf("action not started: %+v", r.runs.snapshot(flow.Name).flow.Status)
			}
			if err := r.runs.claim(flow.Name, []string{"node:owned"}); err != nil {
				t.Fatal(err)
			}
			switch reason {
			case "manager":
				cancel()
			case "delete", "replacement":
				if err := r.Delete(context.Background(), flow); err != nil {
					t.Fatal(err)
				}
				if reason == "replacement" {
					flow.UID, flow.ResourceVersion = "replacement", ""
					if err := r.Create(context.Background(), flow); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(flow)}); err != nil {
					t.Fatal(err)
				}
			case "spec":
				if err := r.Get(context.Background(), client.ObjectKeyFromObject(flow), flow); err != nil {
					t.Fatal(err)
				}
				flow.Generation++
				if err := r.Update(context.Background(), flow); err != nil {
					t.Fatal(err)
				}
				reconcileAsync(t, r, flow)
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("cancellation not delivered")
			}
			if err := r.runs.claim("other", []string{"node:owned"}); err == nil {
				t.Fatal("cancellation released resources before action returned")
			}
			if calls.Load() != 1 {
				t.Fatal("duplicate run while cancellation pending")
			}
			once.Do(func() { close(finish) })
			awaitRunDone(t, r, flow.Name)
			if err := r.runs.claim("other", []string{"node:owned"}); err != nil {
				t.Fatal("finished run retained resources")
			}
			if reason == "manager" {
				select {
				case <-stopped:
				case <-time.After(time.Second):
					t.Fatal("manager did not join")
				}
			}
		})
	}
}

type failingStatusClient struct{ client.Client }
type failingStatusWriter struct{ client.SubResourceWriter }

func (c failingStatusClient) Status() client.SubResourceWriter {
	return failingStatusWriter{c.Client.Status()}
}
func (w failingStatusWriter) Patch(context.Context, client.Object, client.Patch, ...client.SubResourcePatchOption) error {
	return errors.New("status unavailable")
}

func TestAsyncFlowRetainsUnpublishedCompletion(t *testing.T) {
	r, _, _, _ := asyncFlowFixture(t)
	flow := createAsyncFlow(t, r, "status-retry")
	var calls atomic.Int32
	r.ExecutorRunner = storageTestRunner(func(context.Context, executor.Action) (executor.ActionOutcome, error) {
		calls.Add(1)
		return executor.ActionOutcome{Outcome: executor.OutcomeSucceeded}, nil
	})
	reconcileAsync(t, r, flow)
	awaitRunDone(t, r, flow.Name)
	original := r.Client
	r.Client = failingStatusClient{original}
	for range 3 {
		if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: client.ObjectKeyFromObject(flow)}); err == nil {
			t.Fatal("status error discarded")
		}
		if run := r.runs.snapshot(flow.Name); run == nil || !run.done {
			t.Fatal("completion lost before successful publication")
		}
	}
	r.Client = original
	current := reconcileAsync(t, r, flow)
	if current.Status.LastExecution == nil || current.Status.LastExecution.Phase != power.ShutdownExecutionPhaseCompleted || calls.Load() != 2 {
		t.Fatal("completion retry executed actions again")
	}
	if r.runs.snapshot(flow.Name) != nil {
		t.Fatal("successful status publication retained slot")
	}
}

func TestAsyncFlowConflictingExecutionDefersWithoutEffects(t *testing.T) {
	r, _, _, _ := asyncFlowFixture(t)
	a, b := createAsyncFlow(t, r, "owner"), createAsyncFlow(t, r, "contender")
	for _, flow := range []*power.ShutdownFlow{a, b} {
		for i := range flow.Spec.Groups {
			flow.Spec.Groups[i].Action = power.ShutdownStepScaleWorkload
		}
		if err := r.Update(context.Background(), flow); err != nil {
			t.Fatal(err)
		}
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(release) }) })
	var contenderCalls atomic.Int32
	r.ExecutorRunner = storageTestRunner(func(ctx context.Context, action executor.Action) (executor.ActionOutcome, error) {
		if action.ShutdownFlow == b.Name {
			contenderCalls.Add(1)
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
		t.Fatal("owner did not start")
	}
	reconcileAsync(t, r, b)
	awaitRunDone(t, r, b.Name)
	current := reconcileAsync(t, r, b)
	condition := meta.FindStatusCondition(current.Status.Conditions, power.ConditionExecutionReady)
	if condition == nil || condition.Reason != "ExecutionConflict" || contenderCalls.Load() != 0 {
		t.Fatalf("conflicting flow executed: %+v", current.Status)
	}
	releaseOnce.Do(func() { close(release) })
	awaitRunDone(t, r, a.Name)
	reconcileAsync(t, r, a)
	reconcileAsync(t, r, b)
	awaitRunDone(t, r, b.Name)
	current = reconcileAsync(t, r, b)
	if current.Status.LastExecution == nil || current.Status.LastExecution.Phase != power.ShutdownExecutionPhaseCompleted || contenderCalls.Load() != 2 {
		t.Fatalf("contender did not retry after release: %+v", current.Status)
	}
}
