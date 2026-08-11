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
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/predicate"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

// upsDeviceTriggerRelevantPredicate admits only the UPSDevice changes that can
// change a ShutdownFlow's verdict (F-42).
//
// The unpredicated watch this replaces re-enqueued every flow on every telemetry
// poll -- one per device every 5-15 seconds -- and each reconcile did a Postgres
// audit round trip whether or not anything the trigger logic reads had moved. A
// 10h production pull showed 1,516 conflict errors, 744 against ShutdownFlow.
// F-31 fixed the conflicts by making status writes patches; this removes the work
// underneath them.
//
// The admitted set is derived from what the trigger path actually consumes, in
// triggerInputsFromDevices: spec.powerDomains, status.phase, status.runtimeSeconds
// and status.batteryChargePercent. Deliberately excluded are lastPollTime,
// lastStatus, and loadPercent. lastPollTime is the churn source -- it moves on
// every poll by definition -- and while it is copied onto the evaluator's UPSState,
// nothing in evaluation reads it: staleness reaches the evaluator through
// status.phase, which the UPSDevice controller derives.
//
// Erring toward admitting is deliberate. A missed enqueue means a flow evaluating
// against telemetry that has already moved, which is a correctness failure during
// an outage; a surplus enqueue only costs work. So an object that cannot be
// asserted about -- a wrong type, an unreadable old state -- is admitted rather
// than dropped.
func upsDeviceTriggerRelevantPredicate() predicate.Predicate {
	return predicate.Funcs{
		CreateFunc:  func(event.CreateEvent) bool { return true },
		DeleteFunc:  func(event.DeleteEvent) bool { return true },
		GenericFunc: func(event.GenericEvent) bool { return true },
		UpdateFunc: func(e event.UpdateEvent) bool {
			oldDevice, oldOK := e.ObjectOld.(*powerv1alpha1.UPSDevice)
			newDevice, newOK := e.ObjectNew.(*powerv1alpha1.UPSDevice)
			if !oldOK || !newOK {
				return true
			}
			return upsDeviceTriggerInputsChanged(oldDevice, newDevice)
		},
	}
}

// upsDeviceTriggerInputsChanged reports whether anything a ShutdownFlow reads from
// this device has moved.
func upsDeviceTriggerInputsChanged(oldDevice, newDevice *powerv1alpha1.UPSDevice) bool {
	// A spec edit can change which flows this device even belongs to, so it is
	// always relevant regardless of telemetry.
	if oldDevice.Generation != newDevice.Generation {
		return true
	}
	if !equalStringSlices(oldDevice.Spec.PowerDomains, newDevice.Spec.PowerDomains) {
		return true
	}
	if oldDevice.Status.Phase != newDevice.Status.Phase {
		return true
	}
	if !equalInt64Pointers(oldDevice.Status.RuntimeSeconds, newDevice.Status.RuntimeSeconds) {
		return true
	}
	return !equalInt32Pointers(oldDevice.Status.BatteryChargePercent, newDevice.Status.BatteryChargePercent)
}

// equalStringSlices compares order-sensitively. Domain membership is authored, and
// a reordering is a spec edit worth reacting to rather than noise to normalize away.
func equalStringSlices(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// equalInt64Pointers treats a value appearing or disappearing as a change, which is
// the point: telemetry going absent is exactly what PL-32 needs the flow to notice.
func equalInt64Pointers(left, right *int64) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}

func equalInt32Pointers(left, right *int32) bool {
	if left == nil || right == nil {
		return left == right
	}
	return *left == *right
}
