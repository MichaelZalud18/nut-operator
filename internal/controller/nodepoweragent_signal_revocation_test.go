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
	"encoding/json"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/nodeagent"
)

const (
	revocationFlowName  = "rack-a-shutdown"
	revocationAgentName = "rack-a-agents"
	revocationNamespace = "power-system"
)

func revocationSignal(nodeName, executionID string, written time.Time) nodeagent.ShutdownSignal {
	return nodeagent.ShutdownSignal{
		ExecutionID:        executionID,
		NodeName:           nodeName,
		PlanConfigHash:     "plan-abc",
		Reason:             "ReleaseApproved",
		SelectedUPSDevices: []string{"rack-a-ups"},
		ShutdownFlow:       revocationFlowName,
		Timestamp:          written.UTC().Format(time.RFC3339Nano),
	}
}

func revocationFlow(executionID string, completedAt time.Time, triggerActive bool, phase powerv1alpha1.ShutdownExecutionPhase) *powerv1alpha1.ShutdownFlow {
	completed := metav1.NewTime(completedAt.UTC())
	return &powerv1alpha1.ShutdownFlow{
		ObjectMeta: metav1.ObjectMeta{Name: revocationFlowName},
		Status: powerv1alpha1.ShutdownFlowStatus{
			LastExecution: &powerv1alpha1.ShutdownExecutionStatus{
				ExecutionID:   executionID,
				TriggerActive: triggerActive,
				Phase:         phase,
				CompletedAt:   &completed,
			},
		},
	}
}

func TestSignalIsRevokedOnceItsEpisodeIsOver(t *testing.T) {
	written := time.Date(2026, 8, 14, 3, 0, 0, 0, time.UTC)
	flow := revocationFlow("exec-1", written.Add(2*time.Second), false, powerv1alpha1.ShutdownExecutionPhaseCompleted)

	if signalStillAuthorized(revocationSignal("node-a", "exec-1", written), flow) {
		t.Fatal("a signal whose trigger episode has ended is still authorized; the file outlives the halt it authorized")
	}
}

func TestSignalSurvivesWhileItsEpisodeIsStillLive(t *testing.T) {
	written := time.Date(2026, 8, 14, 3, 0, 0, 0, time.UTC)
	flow := revocationFlow("exec-1", written.Add(2*time.Second), true, powerv1alpha1.ShutdownExecutionPhaseCompleted)

	// The trigger is still eligible, so the node was told to halt and has not. A pod that restarts
	// here should read the signal and finish the job.
	if !signalStillAuthorized(revocationSignal("node-a", "exec-1", written), flow) {
		t.Fatal("a signal was revoked while its trigger episode is still active; this cancels a shutdown mid-flow")
	}
}

// The window recordShutdownFlowExecution leaves open: the runner writes the signal Secret, the whole
// executor runs, and only then does LastExecution land. A reconcile in between sees an execution
// record older than the signal in front of it.
func TestSignalNewerThanTheExecutionRecordSurvives(t *testing.T) {
	previous := time.Date(2026, 8, 14, 2, 0, 0, 0, time.UTC)
	flow := revocationFlow("exec-previous", previous, false, powerv1alpha1.ShutdownExecutionPhaseCompleted)
	written := previous.Add(time.Hour)

	if !signalStillAuthorized(revocationSignal("node-a", "exec-now", written), flow) {
		t.Fatal("a signal newer than the flow's last execution record was revoked; that is a live shutdown being cancelled by a status lag")
	}
}

func TestSignalFromASupersededExecutionIsRevoked(t *testing.T) {
	written := time.Date(2026, 8, 14, 3, 0, 0, 0, time.UTC)
	// A later episode is live, but this signal belongs to the one before it.
	flow := revocationFlow("exec-2", written.Add(time.Hour), true, powerv1alpha1.ShutdownExecutionPhaseCompleted)

	if signalStillAuthorized(revocationSignal("node-a", "exec-1", written), flow) {
		t.Fatal("a signal from a superseded execution is still authorized")
	}
}

func TestSignalIsRevokedWhenItsFlowIsGone(t *testing.T) {
	written := time.Date(2026, 8, 14, 3, 0, 0, 0, time.UTC)

	if signalStillAuthorized(revocationSignal("node-a", "exec-1", written), nil) {
		t.Fatal("a signal naming a ShutdownFlow that no longer exists is still authorized")
	}
}

func TestSignalWithNoExecutionRecordSurvives(t *testing.T) {
	written := time.Date(2026, 8, 14, 3, 0, 0, 0, time.UTC)
	flow := &powerv1alpha1.ShutdownFlow{ObjectMeta: metav1.ObjectMeta{Name: revocationFlowName}}

	if !signalStillAuthorized(revocationSignal("node-a", "exec-1", written), flow) {
		t.Fatal("a signal was revoked against a flow with no execution evidence; absence of a record is not evidence the episode ended")
	}
}

func TestUnusableSignalsAreRevokedRatherThanLingering(t *testing.T) {
	written := time.Date(2026, 8, 14, 3, 0, 0, 0, time.UTC)
	live := revocationFlow("exec-1", written.Add(2*time.Second), true, powerv1alpha1.ShutdownExecutionPhaseCompleted)

	cases := map[string]nodeagent.ShutdownSignal{
		"empty payload":       {},
		"no execution ID":     {NodeName: "node-a", ShutdownFlow: revocationFlowName, Timestamp: written.Format(time.RFC3339Nano)},
		"no node name":        {ExecutionID: "exec-1", ShutdownFlow: revocationFlowName, Timestamp: written.Format(time.RFC3339Nano)},
		"no flow":             {ExecutionID: "exec-1", NodeName: "node-a", Timestamp: written.Format(time.RFC3339Nano)},
		"unparseable stamp":   {ExecutionID: "exec-1", NodeName: "node-a", ShutdownFlow: revocationFlowName, Timestamp: "whenever"},
		"no timestamp at all": {ExecutionID: "exec-1", NodeName: "node-a", ShutdownFlow: revocationFlowName},
	}
	for name, payload := range cases {
		if signalStillAuthorized(payload, live) {
			t.Errorf("%s: a signal InspectSignal would refuse is being kept in the channel", name)
		}
	}
}

func revocationScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add corev1 to scheme: %v", err)
	}
	if err := powerv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add powerv1alpha1 to scheme: %v", err)
	}
	return scheme
}

func revocationSecret(t *testing.T, signals map[string]nodeagent.ShutdownSignal) *corev1.Secret {
	t.Helper()
	agent := &powerv1alpha1.NodePowerAgent{ObjectMeta: metav1.ObjectMeta{Name: revocationAgentName}}
	data := map[string][]byte{
		nodeagent.DeliveryChannelMarker: []byte("/" + revocationAgentName + "@1"),
	}
	for node, payload := range signals {
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("encode signal for %s: %v", node, err)
		}
		data[nodePowerAgentSignalKey(node)] = encoded
	}
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: revocationNamespace,
			Name:      nodePowerAgentSignalSecretName(agent),
		},
		Type: corev1.SecretTypeOpaque,
		Data: data,
	}
}

func TestRevokedSignalKeysWithdrawsOnlySpentSignals(t *testing.T) {
	written := time.Date(2026, 8, 14, 3, 0, 0, 0, time.UTC)
	spentFlow := revocationFlow("exec-1", written.Add(2*time.Second), false, powerv1alpha1.ShutdownExecutionPhaseCompleted)
	secret := revocationSecret(t, map[string]nodeagent.ShutdownSignal{
		"node-a": revocationSignal("node-a", "exec-1", written),
		"node-b": revocationSignal("node-b", "exec-1", written),
	})

	reconciler := &NodePowerAgentReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(revocationScheme(t)).
			WithObjects(secret, spentFlow).
			Build(),
	}
	agent := &powerv1alpha1.NodePowerAgent{ObjectMeta: metav1.ObjectMeta{Name: revocationAgentName}}

	revoked, err := reconciler.revokedSignalKeys(context.Background(), agent, revocationNamespace)
	if err != nil {
		t.Fatalf("revokedSignalKeys returned error: %v", err)
	}
	for _, node := range []string{"node-a", "node-b"} {
		if _, found := revoked[nodePowerAgentSignalKey(node)]; !found {
			t.Errorf("signal for %s survived its episode", node)
		}
	}
	if _, found := revoked[nodeagent.DeliveryChannelMarker]; found {
		t.Fatal("the delivery-channel marker was revoked; readiness can no longer tell a quiet channel from a missing one (F-86)")
	}
}

func TestRevokedSignalKeysLeavesALiveEpisodeAlone(t *testing.T) {
	written := time.Date(2026, 8, 14, 3, 0, 0, 0, time.UTC)
	liveFlow := revocationFlow("exec-1", written.Add(2*time.Second), true, powerv1alpha1.ShutdownExecutionPhaseCompleted)
	secret := revocationSecret(t, map[string]nodeagent.ShutdownSignal{
		"node-a": revocationSignal("node-a", "exec-1", written),
	})

	reconciler := &NodePowerAgentReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(revocationScheme(t)).
			WithObjects(secret, liveFlow).
			Build(),
	}
	agent := &powerv1alpha1.NodePowerAgent{ObjectMeta: metav1.ObjectMeta{Name: revocationAgentName}}

	revoked, err := reconciler.revokedSignalKeys(context.Background(), agent, revocationNamespace)
	if err != nil {
		t.Fatalf("revokedSignalKeys returned error: %v", err)
	}
	if len(revoked) != 0 {
		t.Fatalf("a live episode had %d signal(s) withdrawn: %v", len(revoked), revoked)
	}
}

func TestRevokedSignalKeysToleratesAMissingSecret(t *testing.T) {
	reconciler := &NodePowerAgentReconciler{
		Client: fake.NewClientBuilder().WithScheme(revocationScheme(t)).Build(),
	}
	agent := &powerv1alpha1.NodePowerAgent{ObjectMeta: metav1.ObjectMeta{Name: revocationAgentName}}

	revoked, err := reconciler.revokedSignalKeys(context.Background(), agent, revocationNamespace)
	if err != nil {
		t.Fatalf("revokedSignalKeys returned error for a Secret that does not exist yet: %v", err)
	}
	if len(revoked) != 0 {
		t.Fatalf("expected nothing to revoke, got %v", revoked)
	}
}

// The end-to-end shape: the Secret the actuator projects loses the spent key and keeps the marker.
func TestEnsureSignalSecretWithdrawsSpentSignalsAndKeepsTheMarker(t *testing.T) {
	written := time.Date(2026, 8, 14, 3, 0, 0, 0, time.UTC)
	spentFlow := revocationFlow("exec-1", written.Add(2*time.Second), false, powerv1alpha1.ShutdownExecutionPhaseCompleted)
	secret := revocationSecret(t, map[string]nodeagent.ShutdownSignal{
		"node-a": revocationSignal("node-a", "exec-1", written),
	})
	scheme := revocationScheme(t)

	agent := &powerv1alpha1.NodePowerAgent{ObjectMeta: metav1.ObjectMeta{Name: revocationAgentName, Generation: 3}}
	reconciler := &NodePowerAgentReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(secret, spentFlow, agent).
			Build(),
		Scheme: scheme,
	}

	updated, err := reconciler.ensureNodePowerAgentSignalSecret(context.Background(), agent, revocationNamespace)
	if err != nil {
		t.Fatalf("ensureNodePowerAgentSignalSecret returned error: %v", err)
	}
	if _, found := updated.Data[nodePowerAgentSignalKey("node-a")]; found {
		t.Error("a spent signal is still projected to the node; a restarting actuator pod will halt it again")
	}
	if _, found := updated.Data[nodeagent.DeliveryChannelMarker]; !found {
		t.Error("the delivery-channel marker is gone; an empty channel is now indistinguishable from a missing one (F-86)")
	}
}
