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
	"bytes"
	"context"
	"maps"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

// nutServerRenderRelevantPredicate admits only the UPSDevice changes that can
// change what a NUTServer renders (F-43).
//
// The render path reads the device's spec -- driver, port, driverOptions,
// credentialSecretRef, upstreamNUT, simulation -- and its labels, which is what
// spec.deviceSelector matches on. It reads no status at all, so the telemetry
// churn that F-42 had to filter out for ShutdownFlow is excluded here by the same
// rule rather than by a second list of fields: a status-only update cannot change
// a rendered config.
//
// Generation stands in for the whole spec because the API server increments it on
// every spec write. Labels are compared directly, since a label edit is a metadata
// write and does not move the generation -- and relabelling a device is exactly how
// an operator moves it between servers.
func nutServerRenderRelevantPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return true },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			if e.ObjectOld == nil || e.ObjectNew == nil {
				// Erring toward admitting, as in F-42: a surplus reconcile costs a render, a
				// missed one leaves the operand serving a configuration nobody asked for.
				return true
			}
			if e.ObjectOld.GetGeneration() != e.ObjectNew.GetGeneration() {
				return true
			}
			return !labels.Equals(labels.Set(e.ObjectOld.GetLabels()), labels.Set(e.ObjectNew.GetLabels()))
		},
	}
}

// secretDataChangedPredicate admits Secret updates only when the contents moved.
//
// The Secret informer is cluster-wide because Owns(&corev1.Secret{}) already
// establishes one, so this watch delivers every Secret event in the cluster --
// service-account tokens, Helm release records, unrelated workloads. A
// metadata-only update cannot change a rendered NUT config, and filtering here
// keeps the mapping lookup off that traffic entirely rather than doing a list per
// event to discover it was irrelevant.
//
// Creates and deletes are always admitted: a referenced Secret appearing or
// vanishing is exactly the drift this watch exists to catch.
func secretDataChangedPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return true },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldSecret, oldOK := e.ObjectOld.(*corev1.Secret)
			newSecret, newOK := e.ObjectNew.(*corev1.Secret)
			if !oldOK || !newOK {
				return true
			}
			return !maps.EqualFunc(oldSecret.Data, newSecret.Data, bytes.Equal)
		},
	}
}

// nutServerRequestsForUPSDevice enqueues the NUTServers that select this device
// (F-43).
//
// Answers only "does this server select the object I was handed". Both sides of a
// relabelling are covered without any bookkeeping here, because
// EnqueueRequestsFromMapFunc runs the map function over ObjectOld and ObjectNew on
// every update and dedupes the union: the old labels reach the server losing the
// device, the new labels reach the one gaining it.
func (r *NUTServerReconciler) nutServerRequestsForUPSDevice(ctx context.Context, obj client.Object) []reconcile.Request {
	log := logf.FromContext(ctx)

	device, ok := obj.(*powerv1alpha1.UPSDevice)
	if !ok {
		return nil
	}

	var servers powerv1alpha1.NUTServerList
	if err := r.List(ctx, &servers); err != nil {
		log.Error(err, "Failed to list NUTServer resources after UPSDevice change", "device", device.Name)
		return nil
	}

	requests := make([]reconcile.Request, 0, len(servers.Items))
	for _, server := range servers.Items {
		if nutServerWatchesDevice(&server, device) {
			requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: server.Name}})
		}
	}
	return requests
}

// nutServerRequestsForSecret enqueues the NUTServers whose selected devices name
// this Secret as their credential source (F-43).
//
// Owns(&corev1.Secret{}) covers only Secrets carrying an owner reference back to
// the NUTServer. A user-supplied credentialSecretRef target has none, so without
// this the contents of a rotated Secret reach the operand only when some unrelated
// reconcile happens to fire, with nothing reporting the drift in between.
//
// This is what makes credential rotation an operator concern even though rotating
// is an operations action: the operator's duty is not to rotate anything, it is to
// notice the referenced Secret changed and re-render.
func (r *NUTServerReconciler) nutServerRequestsForSecret(ctx context.Context, obj client.Object) []reconcile.Request {
	log := logf.FromContext(ctx)

	secret, ok := obj.(*corev1.Secret)
	if !ok {
		return nil
	}

	var devices powerv1alpha1.UPSDeviceList
	if err := r.List(ctx, &devices); err != nil {
		log.Error(err, "Failed to list UPSDevice resources after Secret change", "secret", secret.Name, "namespace", secret.Namespace)
		return nil
	}

	referencing := make([]powerv1alpha1.UPSDevice, 0, len(devices.Items))
	for _, device := range devices.Items {
		if deviceUsesCredentialSecret(&device, secret) {
			referencing = append(referencing, device)
		}
	}
	if len(referencing) == 0 {
		return nil
	}

	var servers powerv1alpha1.NUTServerList
	if err := r.List(ctx, &servers); err != nil {
		log.Error(err, "Failed to list NUTServer resources after Secret change", "secret", secret.Name, "namespace", secret.Namespace)
		return nil
	}

	requests := make([]reconcile.Request, 0, len(servers.Items))
	for _, server := range servers.Items {
		for i := range referencing {
			if nutServerWatchesDevice(&server, &referencing[i]) {
				requests = append(requests, reconcile.Request{NamespacedName: types.NamespacedName{Name: server.Name}})
				break
			}
		}
	}
	return requests
}

// nutServerWatchesDevice adapts nutServerSpecSelectsDevice for watch mapping.
//
// The spec is asked directly rather than through nutServerSelectsDevice, whose
// status short-circuit is correct for reconcile and wrong here: a device that has
// just been relabelled into a selector is absent from the recorded selection by
// definition, so preferring that record would drop the arrival half of every move.
//
// A malformed deviceSelector matches nothing rather than everything. An invalid
// selector is already a reconcile error the server surfaces on its own status, and
// treating it as a wildcard would turn one bad selector into cluster-wide churn.
func nutServerWatchesDevice(server *powerv1alpha1.NUTServer, device *powerv1alpha1.UPSDevice) bool {
	selected, err := nutServerSpecSelectsDevice(server, device)
	if err != nil {
		return false
	}
	return selected
}

// deviceUsesCredentialSecret reports whether the device names this exact Secret.
//
// Namespace is compared as well as name. The render path requires the reference to
// resolve inside the operand namespace, so a same-named Secret elsewhere is a
// different object and must not trigger a re-render.
func deviceUsesCredentialSecret(device *powerv1alpha1.UPSDevice, secret *corev1.Secret) bool {
	ref := device.Spec.CredentialSecretRef
	if ref == nil {
		return false
	}
	return ref.Name == secret.Name && ref.Namespace == secret.Namespace
}
