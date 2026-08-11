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
	"sort"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

func nutServerWatchScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := powerv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("add power scheme: %v", err)
	}
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("add core scheme: %v", err)
	}
	return scheme
}

func assertSameServers(t *testing.T, requests []reconcile.Request, want []string) {
	t.Helper()
	got := requestNames(t, requests)
	sort.Strings(got)
	sort.Strings(want)
	if len(got) != len(want) {
		t.Fatalf("requests = %v, want %v", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("requests = %v, want %v", got, want)
		}
	}
}

func watchTestDevice(name string, labels map[string]string) *powerv1alpha1.UPSDevice {
	return &powerv1alpha1.UPSDevice{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: labels, Generation: 1},
	}
}

func watchTestServer(name string, spec powerv1alpha1.NUTServerSpec) *powerv1alpha1.NUTServer {
	return &powerv1alpha1.NUTServer{
		ObjectMeta: metav1.ObjectMeta{Name: name, Generation: 1},
		Spec:       spec,
	}
}

func requestNames(t *testing.T, requests []reconcile.Request) []string {
	t.Helper()
	names := make([]string, 0, len(requests))
	for _, request := range requests {
		names = append(names, request.Name)
	}
	return names
}

// F-43: the render reads UPSDevice specs, so a spec edit has to reach the server. Status does not
// reach it at all, which is what keeps the telemetry churn F-42 documented off this watch.
func TestNUTServerRenderPredicateAdmitsSpecAndLabelEditsOnly(t *testing.T) {
	base := watchTestDevice("ups-a", map[string]string{"rack": "a"})

	specEdited := base.DeepCopy()
	specEdited.Generation = 2

	relabelled := base.DeepCopy()
	relabelled.Labels = map[string]string{"rack": "b"}

	statusOnly := base.DeepCopy()
	runtime := int64(900)
	statusOnly.Status.RuntimeSeconds = &runtime
	statusOnly.Status.Phase = powerv1alpha1.UPSDevicePhaseOnBattery
	pollTime := metav1.Now()
	statusOnly.Status.LastPollTime = &pollTime

	predicate := nutServerRenderRelevantPredicate()

	for name, testCase := range map[string]struct {
		updated *powerv1alpha1.UPSDevice
		admit   bool
	}{
		"spec edit":        {updated: specEdited, admit: true},
		"relabelled":       {updated: relabelled, admit: true},
		"telemetry poll":   {updated: statusOnly, admit: false},
		"nothing happened": {updated: base.DeepCopy(), admit: false},
	} {
		t.Run(name, func(t *testing.T) {
			got := predicate.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: testCase.updated})
			if got != testCase.admit {
				t.Fatalf("admitted = %v, want %v", got, testCase.admit)
			}
		})
	}
}

// The Secret informer is cluster-wide, so this predicate is what keeps unrelated Secret traffic --
// service-account tokens, Helm release records -- from reaching the mapping lookup at all.
func TestSecretPredicateAdmitsContentChangesOnly(t *testing.T) {
	base := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "ups-creds", Namespace: "power-system"},
		Data:       map[string][]byte{"username": []byte("monuser"), "password": []byte("old")},
	}

	rotated := base.DeepCopy()
	rotated.Data["password"] = []byte("new")

	keyAdded := base.DeepCopy()
	keyAdded.Data["extra"] = []byte("value")

	annotated := base.DeepCopy()
	annotated.Annotations = map[string]string{"kubectl.kubernetes.io/last-applied-configuration": "{}"}

	predicate := secretDataChangedPredicate()

	for name, testCase := range map[string]struct {
		updated *corev1.Secret
		admit   bool
	}{
		"rotated password": {updated: rotated, admit: true},
		"key added":        {updated: keyAdded, admit: true},
		"annotation only":  {updated: annotated, admit: false},
		"identical":        {updated: base.DeepCopy(), admit: false},
	} {
		t.Run(name, func(t *testing.T) {
			got := predicate.Update(event.UpdateEvent{ObjectOld: base, ObjectNew: testCase.updated})
			if got != testCase.admit {
				t.Fatalf("admitted = %v, want %v", got, testCase.admit)
			}
		})
	}
}

// Both selection paths have to map back, and a server that selects nothing relevant must not be
// enqueued -- otherwise one device edit reconciles every server in the cluster.
func TestNUTServerRequestsForUPSDeviceCoversBothSelectionPaths(t *testing.T) {
	device := watchTestDevice("ups-a", map[string]string{"rack": "a"})

	byRef := watchTestServer("server-by-ref", powerv1alpha1.NUTServerSpec{
		DeviceRefs: []powerv1alpha1.ObjectNameReference{{Name: "ups-a"}},
	})
	bySelector := watchTestServer("server-by-selector", powerv1alpha1.NUTServerSpec{
		DeviceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"rack": "a"}},
	})
	unrelated := watchTestServer("server-unrelated", powerv1alpha1.NUTServerSpec{
		DeviceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"rack": "z"}},
	})

	reconciler := &NUTServerReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(nutServerWatchScheme(t)).
			WithObjects(device, byRef, bySelector, unrelated).
			Build(),
	}

	assertSameServers(t, reconciler.nutServerRequestsForUPSDevice(context.Background(), device),
		[]string{"server-by-ref", "server-by-selector"})
}

// A device relabelled out of a selector no longer matches the spec, so only the server's record of
// what it last selected can still reach it. Without that the server keeps serving a device that is
// no longer its own.
func TestNUTServerRequestsForUPSDeviceReachesTheServerThatJustLostIt(t *testing.T) {
	device := watchTestDevice("ups-a", map[string]string{"rack": "b"})

	former := watchTestServer("server-former", powerv1alpha1.NUTServerSpec{
		DeviceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"rack": "a"}},
	})
	former.Status.SelectedDevices = []string{"ups-a"}
	former.Status.ObservedGeneration = former.Generation

	arriving := watchTestServer("server-arriving", powerv1alpha1.NUTServerSpec{
		DeviceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"rack": "b"}},
	})
	arriving.Status.SelectedDevices = nil
	arriving.Status.ObservedGeneration = arriving.Generation

	reconciler := &NUTServerReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(nutServerWatchScheme(t)).
			WithObjects(device, former, arriving).
			Build(),
	}

	assertSameServers(t, reconciler.nutServerRequestsForUPSDevice(context.Background(), device),
		[]string{"server-former", "server-arriving"})
}

// The finding itself: Owns() never matches a user-supplied credentialSecretRef target, so a rotated
// Secret has to reach the server through the device that names it.
func TestNUTServerRequestsForSecretFollowsCredentialReferences(t *testing.T) {
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "ups-creds", Namespace: "power-system"}}

	referencing := watchTestDevice("ups-a", map[string]string{"rack": "a"})
	referencing.Spec.CredentialSecretRef = &powerv1alpha1.NamespacedNameReference{
		Name: "ups-creds", Namespace: "power-system",
	}
	unreferencing := watchTestDevice("ups-b", map[string]string{"rack": "a"})

	server := watchTestServer("server-a", powerv1alpha1.NUTServerSpec{
		DeviceSelector: &metav1.LabelSelector{MatchLabels: map[string]string{"rack": "a"}},
	})
	elsewhere := watchTestServer("server-elsewhere", powerv1alpha1.NUTServerSpec{
		DeviceRefs: []powerv1alpha1.ObjectNameReference{{Name: "ups-b"}},
	})

	reconciler := &NUTServerReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(nutServerWatchScheme(t)).
			WithObjects(secret, referencing, unreferencing, server, elsewhere).
			Build(),
	}

	assertSameServers(t, reconciler.nutServerRequestsForSecret(context.Background(), secret),
		[]string{"server-a"})
}

// The render requires credentialSecretRef to resolve inside the operand namespace, so a same-named
// Secret in another namespace is a different object entirely and must not trigger a re-render.
func TestNUTServerRequestsForSecretIgnoresASameNamedSecretElsewhere(t *testing.T) {
	device := watchTestDevice("ups-a", nil)
	device.Spec.CredentialSecretRef = &powerv1alpha1.NamespacedNameReference{
		Name: "ups-creds", Namespace: "power-system",
	}
	server := watchTestServer("server-a", powerv1alpha1.NUTServerSpec{
		DeviceRefs: []powerv1alpha1.ObjectNameReference{{Name: "ups-a"}},
	})

	impostor := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "ups-creds", Namespace: "default"}}

	reconciler := &NUTServerReconciler{
		Client: fake.NewClientBuilder().
			WithScheme(nutServerWatchScheme(t)).
			WithObjects(device, server, impostor).
			Build(),
	}

	if got := reconciler.nutServerRequestsForSecret(context.Background(), impostor); len(got) != 0 {
		t.Fatalf("requests = %v, want none for a Secret outside the operand namespace", requestNames(t, got))
	}
}

// An unparseable selector is already a reconcile error the server reports on its own status.
// Treating it as a wildcard here would turn one bad selector into a reconcile of every server on
// every device event.
func TestAMalformedSelectorMatchesNothingRatherThanEverything(t *testing.T) {
	device := watchTestDevice("ups-a", map[string]string{"rack": "a"})
	server := watchTestServer("server-broken", powerv1alpha1.NUTServerSpec{
		DeviceSelector: &metav1.LabelSelector{
			MatchExpressions: []metav1.LabelSelectorRequirement{{
				Key: "rack", Operator: "NotAnOperator", Values: []string{"a"},
			}},
		},
	})

	if nutServerWatchesDevice(server, device) {
		t.Fatal("a malformed deviceSelector must not behave as a wildcard")
	}
}
