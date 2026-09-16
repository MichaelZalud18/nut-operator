package controller

import (
	"context"
	"errors"
	"testing"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/executor"
	shutdownflowadapter "github.com/MichaelZalud18/nut-operator/internal/shutdownflow"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCompilationAuditDoesNotExecuteOrOwnStorage(t *testing.T) {
	r, flow, bundle := executionScopeFixture(t)
	connector := &fakeAuditConnector{err: errors.New("must not open storage")}
	r.StorageConnector = connector
	r.ExecutorRunner = storageTestRunner(func(context.Context, executor.Action) (executor.ActionOutcome, error) {
		t.Error("audit recording executed an action")
		return executor.ActionOutcome{}, nil
	})
	store := &fakeAuditStore{}
	evaluation := &power.ShutdownTriggerEvaluationStatus{Eligible: true, SelectedUPSDevices: []string{"ups-a"},
		Decisions: []power.ShutdownTriggerDecisionStatus{{Type: power.ShutdownTriggerOnBattery, Eligible: true, SelectedUPSDevices: []string{"ups-a"}}}}
	if err := r.recordShutdownFlowAudit(context.Background(), store, flow, time.Now(), accepted("test"), nil, nil, bundle, nil, nil, "hash", evaluation); err != nil {
		t.Fatal(err)
	}
	if connector.opens != 0 || store.closeCalls != 0 || flow.Status.LastExecution != nil || len(store.actionAttempts) != 0 {
		t.Fatal("audit recording crossed the execution/storage ownership boundary")
	}
	if len(store.shutdownFlowCompilations) != 1 || len(store.shutdownFlowDecisions) == 0 {
		t.Fatal("compilation/decision evidence was not recorded")
	}
}

func TestShutdownWorkerSeparatesEvidenceFromExecution(t *testing.T) {
	writeErr := errors.New("audit write failed")
	closeErr := errors.New("audit close failed")
	openErr := errors.New("audit connection failed")
	for _, tc := range []struct {
		name                                           string
		openErr                                        error
		accepted, eligible, ready, spool, cancelAction bool
		invalidSpool                                   bool
		writeErr, closeErr                             error
		wantActions                                    bool
	}{
		{name: "healthy", accepted: true, eligible: true, ready: true, wantActions: true},
		{name: "write-failure", accepted: true, eligible: true, ready: true, writeErr: writeErr, wantActions: true},
		{name: "close-failure", accepted: true, eligible: true, ready: true, closeErr: closeErr, wantActions: true},
		{name: "cancel-action", accepted: true, eligible: true, ready: true, cancelAction: true, wantActions: true},
		{name: "combined-errors", accepted: true, eligible: true, ready: true, cancelAction: true, writeErr: writeErr, closeErr: closeErr, wantActions: true},
		{name: "ready-open-failure-without-spool", accepted: true, eligible: true, ready: true, openErr: openErr},
		{name: "rejected-with-failed-evidence", eligible: true, ready: true, writeErr: writeErr},
		{name: "ineligible-with-failed-evidence", accepted: true, ready: true, writeErr: writeErr},
		{name: "unready-without-spool", accepted: true, eligible: true},
		{name: "unready-spool-rejected", eligible: true, spool: true},
		{name: "unready-spool-ineligible", accepted: true, spool: true},
		{name: "writer-setup-failure-closes-store", accepted: true, eligible: true, ready: true, spool: true, invalidSpool: true, closeErr: closeErr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, flow, bundle := executionScopeFixture(t)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			cluster := &power.PowerManagementCluster{ObjectMeta: metav1.ObjectMeta{Name: "storage"}}
			cluster.Spec.Storage = power.PowerStorageSpec{Mode: power.PowerStorageExternalPostgres,
				ExternalPostgres: &power.ExternalPostgresStorageSpec{DSNSecretKeyRef: power.SecretKeyReference{Namespace: "power-system", Name: "postgres", Key: "dsn"}},
				AuditSpool:       power.AuditSpoolSpec{Enabled: tc.spool, Path: t.TempDir()}}
			cluster.Status.Storage = power.StorageStatus{Mode: power.PowerStorageExternalPostgres, Ready: tc.ready}
			if tc.invalidSpool {
				cluster.Spec.Storage.AuditSpool.Path = "relative-path"
			}
			if err := r.Create(ctx, cluster); err != nil {
				t.Fatal(err)
			}
			flow.Spec.ManagementClusterRef = &power.ObjectNameReference{Name: cluster.Name}
			if err := r.Create(ctx, flow); err != nil {
				t.Fatal(err)
			}
			store := &fakeAuditStore{writeErr: tc.writeErr, closeErr: tc.closeErr}
			connector := &fakeAuditConnector{store: store, err: tc.openErr}
			r.StorageConnector = connector
			calls := 0
			r.ExecutorRunner = storageTestRunner(func(ctx context.Context, _ executor.Action) (executor.ActionOutcome, error) {
				calls++
				if store.closeCalls != 0 {
					t.Error("store closed before execution returned")
				}
				if tc.cancelAction {
					cancel()
					return executor.ActionOutcome{}, ctx.Err()
				}
				return executor.ActionOutcome{Outcome: executor.OutcomeSucceeded}, nil
			})
			evaluation := &power.ShutdownTriggerEvaluationStatus{Eligible: tc.eligible, SelectedUPSDevices: []string{"ups-a"}}
			compiled := shutdownflowadapter.CompileForEvaluation(flow, bundle, power.PowerShutdownTierPolicySpec{}, nil, nil, evaluation)
			flow.Status.CompiledSteps, flow.Status.CompiledWaves = compiled.Steps, compiled.Waves
			result := accepted("test")
			result.accepted = tc.accepted
			err := r.runShutdownFlow(ctx, flow, result, nil, nil, bundle, compiled.Waves, compiled.Artifact, compiled.ConfigHash, evaluation)
			if (calls > 0) != tc.wantActions {
				t.Fatalf("action calls = %d, want actions %v; error %v", calls, tc.wantActions, err)
			}
			assertShutdownWorkerStorageErrors(t, err, tc.writeErr, tc.closeErr, tc.openErr)
			if tc.wantActions && !tc.cancelAction && (flow.Status.LastExecution == nil || flow.Status.LastExecution.Phase != power.ShutdownExecutionPhaseCompleted) {
				t.Fatalf("evidence failure changed successful action outcome: %+v", flow.Status.LastExecution)
			}
			if tc.cancelAction && !errors.Is(err, context.Canceled) {
				t.Fatalf("lost action cancellation: %v", err)
			}
			if tc.writeErr == nil && tc.closeErr == nil && tc.openErr == nil && !tc.cancelAction && err != nil {
				t.Fatal(err)
			}
			wantCloses := 0
			if tc.ready && tc.openErr == nil {
				wantCloses = 1
			}
			if tc.ready && (connector.opens != 1 || store.closeCalls != wantCloses) {
				t.Fatalf("opens=%d closes=%d", connector.opens, store.closeCalls)
			}
			if !tc.ready && connector.opens != 0 {
				t.Fatal("opened unready storage")
			}
		})
	}
}

func assertShutdownWorkerStorageErrors(t *testing.T, err, writeErr, closeErr, openErr error) {
	t.Helper()
	if writeErr != nil && !errors.Is(err, writeErr) {
		t.Fatalf("lost evidence error: %v", err)
	}
	if closeErr != nil && !errors.Is(err, closeErr) {
		t.Fatalf("lost close error: %v", err)
	}
	if openErr != nil && !errors.Is(err, openErr) {
		t.Fatalf("lost open error: %v", err)
	}
}
