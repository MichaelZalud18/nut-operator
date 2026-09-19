package controller

import (
	"context"
	"encoding/json"
	"time"

	power "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/executor"
	"github.com/MichaelZalud18/nut-operator/internal/kubeactions"
	"github.com/MichaelZalud18/nut-operator/internal/nodeagent"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// EX-35 uses real API objects and the complete publication validator. Readiness
// is synthetic envtest evidence; the declared Nodes are not a running HA cluster.
var _ = Describe("EX-35 real-API quorum publication", func() {
	var ns *corev1.Namespace
	var members []string
	var releases []executor.NodeRelease
	var reconciler *ShutdownFlowReconciler
	var runner kubeactions.Runner
	var runCtx context.Context

	BeforeEach(func() {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, 30*time.Second)
		DeferCleanup(cancel)
		ns = &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{GenerateName: "ex35-"}}
		Expect(k8sClient.Create(runCtx, ns)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, ns)).To(Succeed()) })
		members = nil
		releases = nil
		for _, suffix := range []string{"a", "b", "c"} {
			node := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: ns.Name + "-" + suffix}}
			Expect(k8sClient.Create(runCtx, node)).To(Succeed())
			DeferCleanup(func() { Expect(k8sClient.Delete(ctx, node)).To(Succeed()) })
			node.Status.Conditions = []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}}
			Expect(k8sClient.Status().Update(runCtx, node)).To(Succeed())
			members = append(members, node.Name)
		}
		device := &power.UPSDevice{ObjectMeta: metav1.ObjectMeta{Name: ns.Name}, Spec: power.UPSDeviceSpec{Driver: "dummy-ups"}}
		Expect(k8sClient.Create(runCtx, device)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, device)).To(Succeed()) })
		device.Status.Phase = power.UPSDevicePhaseOnline
		device.Status.LastPollTime = ptr.To(metav1.Now())
		Expect(k8sClient.Status().Update(runCtx, device)).To(Succeed())
		server := &power.NUTServer{ObjectMeta: metav1.ObjectMeta{Name: ns.Name}, Spec: power.NUTServerSpec{DeviceRefs: []power.ObjectNameReference{{Name: device.Name}}}}
		Expect(k8sClient.Create(runCtx, server)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, server)).To(Succeed()) })
		server.Status.SelectedDevices = []string{device.Name}
		Expect(k8sClient.Status().Update(runCtx, server)).To(Succeed())
		agent := &power.NodePowerAgent{ObjectMeta: metav1.ObjectMeta{Name: ns.Name}, Spec: power.NodePowerAgentSpec{
			Namespace: ns.Name, Mode: power.NodePowerAgentModeDryRun,
			NUTServerRefs: []power.ObjectNameReference{{Name: server.Name}},
			Shutdown:      power.AgentShutdownSpec{ActuatorPolicy: power.ActuatorPolicySimulate, RequireFreshTelemetry: ptr.To(true)},
		}}
		Expect(k8sClient.Create(runCtx, agent)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, agent)).To(Succeed()) })
		for _, name := range members {
			pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns.Name}, Spec: corev1.PodSpec{
				NodeName: name, Containers: []corev1.Container{{Name: "actuator", Image: "fixture:unused", Env: []corev1.EnvVar{
					{Name: "POWER_AGENT_MODE", Value: "DryRun"}, {Name: "POWER_ACTUATOR_POLICY", Value: "Simulate"},
				}}},
			}}
			Expect(k8sClient.Create(runCtx, pod)).To(Succeed())
			DeferCleanup(func() { Expect(k8sClient.Delete(ctx, pod, client.GracePeriodSeconds(0))).To(Succeed()) })
			pod.Status.Phase = corev1.PodRunning
			pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
			Expect(k8sClient.Status().Update(runCtx, pod)).To(Succeed())
			agent.Status.NodeStatuses = append(agent.Status.NodeStatuses, power.NodePowerAgentNodeStatus{NodeName: name, PodName: name, Ready: true})
			releases = append(releases, executor.NodeRelease{NodeName: name, NodePowerAgent: agent.Name,
				AgentUID: string(agent.UID), AgentGeneration: agent.Generation, ActuatorPolicy: "Simulate",
				ControlPlaneNodes: members, QuorumMembers: members, SignalSecretNamespace: ns.Name,
				SignalSecretName: nodePowerAgentSignalSecretName(agent), SignalSecretKey: name + ".json",
			})
		}
		agent.Status.ObservedGeneration = agent.Generation
		agent.Status.SelectedNodes = members
		Expect(k8sClient.Status().Update(runCtx, agent)).To(Succeed())
		// client.New is a direct REST client, not an informer cache.
		reader, err := client.New(cfg, client.Options{Scheme: k8sClient.Scheme()})
		Expect(err).NotTo(HaveOccurred())
		reconciler = &ShutdownFlowReconciler{Client: k8sClient, APIReader: reader}
		runner = kubeactions.Runner{Client: k8sClient, ValidateNodeRelease: reconciler.ValidateNodeRelease}
		DeferCleanup(func() {
			Expect(k8sClient.DeleteAllOf(ctx, &corev1.Secret{}, client.InNamespace(ns.Name))).To(Succeed())
		})
	})

	action := func(selected ...executor.NodeRelease) executor.Action {
		return executor.Action{ExecutionID: "ex35-run", ShutdownFlow: "ex35-flow", PlanConfigHash: "ex35-plan",
			Group: executor.Group{Action: executor.ActionAgentShutdown, NodeReleases: selected}}
	}
	readChannel := func(release executor.NodeRelease) corev1.Secret {
		var secret corev1.Secret
		Expect(k8sClient.Get(runCtx, client.ObjectKey{Namespace: ns.Name, Name: release.SignalSecretName}, &secret)).To(Succeed())
		return secret
	}
	assertReceipt := func(out executor.ActionOutcome, selected executor.NodeRelease) {
		Expect(out.SignalResults).To(HaveLen(1))
		receipt := out.SignalResults[0]
		Expect(receipt.Published).To(BeTrue())
		Expect(receipt.NodeName).To(Equal(selected.NodeName))
		Expect(receipt.SignalSecretName).To(Equal(selected.SignalSecretName))
		Expect(receipt.SignalSecretKey).To(Equal(selected.SignalSecretKey))
		var signal nodeagent.ShutdownSignal
		Expect(json.Unmarshal(readChannel(selected).Data[selected.SignalSecretKey], &signal)).To(Succeed())
		Expect(signal.ExecutionID).To(Equal("ex35-run"))
		Expect(signal.NodeName).To(Equal(receipt.NodeName))
		Expect(signal.Timestamp).To(Equal(receipt.IssuedAt.UTC().Format(time.RFC3339Nano)))
	}

	It("counts a pending signal against the margin while all three Nodes remain Ready", func() {
		out, err := runner.RunAction(runCtx, action(releases[0]))
		Expect(err).NotTo(HaveOccurred())
		assertReceipt(out, releases[0])
		before := readChannel(releases[0])
		out, err = runner.RunAction(runCtx, action(releases[1]))
		Expect(err).To(MatchError(ContainSubstring("1 of 3 quorum members")))
		Expect(out.SignalResults).To(HaveLen(1))
		Expect(out.SignalResults[0].Published).To(BeFalse())
		after := readChannel(releases[0])
		Expect(after.ResourceVersion).To(Equal(before.ResourceVersion))
		Expect(after.Data).To(Equal(before.Data))
		for _, name := range members {
			var node corev1.Node
			Expect(k8sClient.Get(runCtx, client.ObjectKey{Name: name}, &node)).To(Succeed())
			Expect(node.Status.Conditions).To(ContainElement(HaveField("Status", corev1.ConditionTrue)))
		}
	})

	It("refuses a release when a declared peer is unavailable", func() {
		var peer corev1.Node
		Expect(k8sClient.Get(runCtx, client.ObjectKey{Name: members[2]}, &peer)).To(Succeed())
		peer.Status.Conditions[0].Status = corev1.ConditionFalse
		Expect(k8sClient.Status().Update(runCtx, &peer)).To(Succeed())
		out, err := runner.RunAction(runCtx, action(releases[0]))
		Expect(err).To(MatchError(ContainSubstring("1 of 3 quorum members")))
		Expect(out.SignalResults[0].Published).To(BeFalse())
		var secrets corev1.SecretList
		Expect(k8sClient.List(runCtx, &secrets, client.InNamespace(ns.Name))).To(Succeed())
		Expect(secrets.Items).To(BeEmpty())
	})

	It("serializes competing agent channels through the active manager publication gate", func() {
		// Give b its own real agent/channel; resourceVersion conflicts cannot protect
		// the shared quorum budget when the two writers target different Secrets.
		var agent power.NodePowerAgent
		Expect(k8sClient.Get(runCtx, client.ObjectKey{Name: ns.Name}, &agent)).To(Succeed())
		second := agent.DeepCopy()
		second.ObjectMeta = metav1.ObjectMeta{Name: ns.Name + "-second"}
		Expect(k8sClient.Create(runCtx, second)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, second)).To(Succeed()) })
		second.Status = agent.Status
		second.Status.ObservedGeneration = second.Generation
		Expect(k8sClient.Status().Update(runCtx, second)).To(Succeed())
		releases[1].NodePowerAgent = second.Name
		releases[1].AgentUID = string(second.UID)
		releases[1].AgentGeneration = second.Generation
		releases[1].SignalSecretName = nodePowerAgentSignalSecretName(second)
		type result struct {
			index int
			out   executor.ActionOutcome
			err   error
		}
		results := make(chan result, 2)
		ready := make(chan struct{}, 2)
		start := make(chan struct{})
		for i := range 2 {
			go func() {
				ready <- struct{}{}
				<-start
				out, err := runner.RunAction(runCtx, action(releases[i]))
				results <- result{i, out, err}
			}()
		}
		for range 2 {
			Eventually(ready, 5*time.Second).Should(Receive())
		}
		close(start)
		succeeded := 0
		for range 2 {
			var got result
			Eventually(results, 15*time.Second).Should(Receive(&got))
			if got.err == nil {
				succeeded++
				assertReceipt(got.out, releases[got.index])
			} else {
				Expect(got.err).To(MatchError(ContainSubstring("1 of 3 quorum members")))
				Expect(got.out.SignalResults[0].Published).To(BeFalse())
			}
		}
		Expect(succeeded).To(Equal(1))
		var secrets corev1.SecretList
		Expect(k8sClient.List(runCtx, &secrets, client.InNamespace(ns.Name))).To(Succeed())
		Expect(secrets.Items).To(HaveLen(1))
		Expect(secrets.Items[0].Data).To(HaveLen(1))
	})

	for _, existing := range []bool{false, true} {
		It("validates every terminal member before publishing the complete batch (existing channel="+map[bool]string{false: "false", true: "true"}[existing]+")", func() {
			for i := range releases {
				releases[i].TerminalHandoff = true
			}
			if existing {
				secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: releases[0].SignalSecretName, Namespace: ns.Name}, Data: map[string][]byte{nodeagent.DeliveryChannelMarker: []byte("fixture")}}
				Expect(k8sClient.Create(runCtx, secret)).To(Succeed())
			}
			var pod corev1.Pod
			Expect(k8sClient.Get(runCtx, client.ObjectKey{Namespace: ns.Name, Name: members[1]}, &pod)).To(Succeed())
			pod.Status.Conditions[0].Status = corev1.ConditionFalse
			Expect(k8sClient.Status().Update(runCtx, &pod)).To(Succeed())
			out, err := runner.RunAction(runCtx, action(releases...))
			Expect(err).To(MatchError(ContainSubstring("agent pod is not currently ready")))
			Expect(out.SignalResults).To(BeEmpty())
			var secrets corev1.SecretList
			Expect(k8sClient.List(runCtx, &secrets, client.InNamespace(ns.Name))).To(Succeed())
			if existing {
				Expect(secrets.Items).To(HaveLen(1))
				Expect(secrets.Items[0].Data).To(Equal(map[string][]byte{nodeagent.DeliveryChannelMarker: []byte("fixture")}))
			} else {
				Expect(secrets.Items).To(BeEmpty())
			}
			pod.Status.Conditions[0].Status = corev1.ConditionTrue
			Expect(k8sClient.Status().Update(runCtx, &pod)).To(Succeed())
			out, err = runner.RunAction(runCtx, action(releases...))
			Expect(err).NotTo(HaveOccurred())
			Expect(out.SignalResults).To(HaveLen(3))
			for i, receipt := range out.SignalResults {
				assertReceipt(executor.ActionOutcome{SignalResults: []executor.NodeSignalResult{receipt}}, releases[i])
			}
			size := 3
			if existing {
				size++
			}
			Expect(readChannel(releases[0]).Data).To(HaveLen(size))
		})
	}
})
