package controller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/audit"
	"github.com/MichaelZalud18/nut-operator/internal/executor"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestShutdownFlowSpoolsWhenDatabaseCannotOpen(t *testing.T) {
	for _, ready := range []bool{false, true} {
		t.Run(map[bool]string{false: "not-ready", true: "open-error"}[ready], func(t *testing.T) {
			r, flow, bundle := executionScopeFixture(t)
			ctx := context.Background()
			flow.Spec.Mode = power.ShutdownFlowModeEnforce
			dir := t.TempDir()
			cluster := &power.PowerManagementCluster{ObjectMeta: metav1.ObjectMeta{Name: "storage"}}
			cluster.Spec.Storage = power.PowerStorageSpec{Mode: power.PowerStorageExternalPostgres,
				ExternalPostgres: &power.ExternalPostgresStorageSpec{DSNSecretKeyRef: power.SecretKeyReference{Namespace: "power-system", Name: "postgres", Key: "dsn"}},
				AuditSpool:       power.AuditSpoolSpec{Enabled: true, Path: dir}}
			cluster.Status.Storage = power.StorageStatus{Mode: power.PowerStorageExternalPostgres, Ready: ready}
			if err := r.Create(ctx, cluster); err != nil {
				t.Fatal(err)
			}
			flow.Spec.ManagementClusterRef = &power.ObjectNameReference{Name: cluster.Name}
			if err := r.Create(ctx, flow); err != nil {
				t.Fatal(err)
			}
			calls := 0
			r.ExecutorRunner = storageTestRunner(func(context.Context, executor.Action) (executor.ActionOutcome, error) {
				calls++
				return executor.ActionOutcome{Outcome: executor.OutcomeSucceeded}, nil
			})
			r.StorageConnector = &fakeAuditConnector{err: errors.New("database unreachable")}
			evaluation := &power.ShutdownTriggerEvaluationStatus{Eligible: true, SelectedUPSDevices: []string{"ups-a"}}
			compiled := compileShutdownFlowForEvaluation(flow, bundle, power.PowerShutdownTierPolicySpec{}, nil, nil, evaluation)
			flow.Status.CompiledSteps, flow.Status.CompiledWaves = compiled.Steps, compiled.Waves
			if err := r.recordShutdownFlowAudit(ctx, flow, accepted("test"), nil, nil, bundle, compiled.Waves, compiled.Artifact, compiled.ConfigHash, evaluation); err != nil {
				t.Fatal(err)
			}
			if flow.Status.LastExecution == nil || flow.Status.LastExecution.Phase != power.ShutdownExecutionPhaseCompleted {
				t.Fatalf("execution: %+v", flow.Status.LastExecution)
			}
			if calls == 0 {
				t.Fatal("database failure prevented enforced actions")
			}
			data, err := os.ReadFile(filepath.Join(dir, "audit-spool.jsonl"))
			if err != nil || !strings.Contains(string(data), `"kind":"shutdownflow_action_attempt"`) {
				t.Fatalf("missing spooled actions: %v", err)
			}
		})
	}
}

type stalledAuditConnector struct{}

type storageTestRunner func(context.Context, executor.Action) (executor.ActionOutcome, error)

func (f storageTestRunner) RunAction(ctx context.Context, action executor.Action) (executor.ActionOutcome, error) {
	return f(ctx, action)
}

func (stalledAuditConnector) OpenAuditStore(ctx context.Context, _ *power.PowerManagementCluster) (audit.Store, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestShutdownStoreOpenIsBounded(t *testing.T) {
	r := &ShutdownFlowReconciler{StorageConnector: stalledAuditConnector{}}
	cluster := &power.PowerManagementCluster{}
	cluster.Spec.Storage.AuditSpool.Enabled = true
	cluster.Status.Storage = power.StorageStatus{Mode: power.PowerStorageExternalPostgres, Ready: true}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	store, err := r.openExecutionAuditStore(ctx, cluster)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > time.Second {
		t.Fatal("open ignored parent deadline")
	}
	if _, err := store.GroupDurations(context.Background(), "flow", "hash", 1); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unavailability lost: %v", err)
	}
}
