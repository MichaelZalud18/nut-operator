package controller

import (
	"context"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/executor"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var _ = Describe("ShutdownFlow manager execution ownership", func() {
	It("publishes progress and completes another flow while an action is blocked", func() {
		ctx := context.Background()
		cleanupShutdownFlowResolverFixture(ctx)
		Expect(createShutdownFlowResolverFixture(ctx)).To(Succeed())
		DeferCleanup(func() { cleanupShutdownFlowResolverFixture(ctx) })
		cluster := &power.PowerManagementCluster{ObjectMeta: metav1.ObjectMeta{Name: shutdownFlowTestPowerClusterName}}
		cluster.Spec.Observability.PublishCadence = &power.PublishCadenceSpec{Active: &metav1.Duration{Duration: time.Second}}
		Expect(k8sClient.Create(ctx, cluster)).To(Succeed())
		cluster.Status.Storage = power.StorageStatus{Ready: true, Mode: power.PowerStorageExternalPostgres}
		Expect(k8sClient.Status().Update(ctx, cluster)).To(Succeed())
		putUPSDeviceOnBattery(ctx, time.Now())
		for _, name := range []string{shutdownFlowTestResourceName, "manager-independent"} {
			flow := &power.ShutdownFlow{ObjectMeta: metav1.ObjectMeta{Name: name, Annotations: map[string]string{"test/approval": "true"}}, Spec: power.ShutdownFlowSpec{
				Mode: power.ShutdownFlowModeEnforce, ManagementClusterRef: &power.ObjectNameReference{Name: cluster.Name},
				CommunicationPaths: []power.FlowCommunicationPath{{Service: "OperatorAPI", Exempt: true}, {Service: "NUT", Exempt: true}},
				Triggers:           []power.ShutdownTrigger{{Type: power.ShutdownTriggerOnBattery}},
				Groups:             []power.ShutdownGroup{{Name: "first", Action: power.ShutdownStepNotify, Before: []string{"second"}}, {Name: "second", Action: power.ShutdownStepNotify}},
			}}
			flow.Spec.Safety.ApprovalAnnotation = "test/approval"
			Expect(k8sClient.Create(ctx, flow)).To(Succeed())
			DeferCleanup(func() { Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, flow))).To(Succeed()) })
		}
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{Scheme: k8sClient.Scheme(), Metrics: metricsserver.Options{BindAddress: "0"}, HealthProbeBindAddress: "0"})
		Expect(err).NotTo(HaveOccurred())
		blocked := make(chan struct{}, 1)
		r := &ShutdownFlowReconciler{Client: mgr.GetClient(), APIReader: mgr.GetAPIReader(), Scheme: mgr.GetScheme(), StorageConnector: &ownedAuditConnector{},
			ExecutorRunner: storageTestRunner(func(ctx context.Context, action executor.Action) (executor.ActionOutcome, error) {
				if action.ShutdownFlow == shutdownFlowTestResourceName && action.Group.Name == "second" {
					blocked <- struct{}{}
					<-ctx.Done()
					return executor.ActionOutcome{}, ctx.Err()
				}
				return executor.ActionOutcome{Outcome: executor.OutcomeSucceeded}, nil
			}),
		}
		Expect(r.SetupWithManager(mgr)).To(Succeed())
		managerCtx, stop := context.WithCancel(ctx)
		stopped := make(chan error, 1)
		go func() { stopped <- mgr.Start(managerCtx) }()
		DeferCleanup(func() { stop(); Eventually(stopped, 10*time.Second).Should(Receive(BeNil())) })
		Eventually(blocked, 10*time.Second).Should(Receive())
		read := func(name string) *power.ShutdownFlow {
			flow := &power.ShutdownFlow{}
			Expect(k8sClient.Get(ctx, client.ObjectKey{Name: name}, flow)).To(Succeed())
			return flow
		}
		Eventually(func() power.ShutdownFlowPhase { return read("manager-independent").Status.Phase }, 10*time.Second).Should(Equal(power.ShutdownFlowPhaseCompleted))
		Eventually(func() int32 {
			flow := read(shutdownFlowTestResourceName)
			if flow.Status.LastExecution == nil {
				return 0
			}
			return flow.Status.LastExecution.GroupCount
		}, 10*time.Second).Should(Equal(int32(1)))
		first := read(shutdownFlowTestResourceName).Status.LastPublishTime.DeepCopy()
		Expect(first).NotTo(BeNil())
		Eventually(func() bool {
			current := read(shutdownFlowTestResourceName)
			return current.Status.LastPublishTime.After(first.Time) && current.Status.Phase == power.ShutdownFlowPhaseRunning
		}, 5*time.Second).Should(BeTrue())
	})
})
