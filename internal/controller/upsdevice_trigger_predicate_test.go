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
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
)

func triggerDevice() *powerv1alpha1.UPSDevice {
	runtimeSeconds := int64(1800)
	charge := int32(97)
	polled := metav1.NewTime(time.Date(2026, 8, 9, 20, 0, 0, 0, time.UTC))
	return &powerv1alpha1.UPSDevice{
		ObjectMeta: metav1.ObjectMeta{Name: "rack-a", Generation: 1},
		Spec:       powerv1alpha1.UPSDeviceSpec{PowerDomains: []string{"core"}},
		Status: powerv1alpha1.UPSDeviceStatus{
			Phase:                powerv1alpha1.UPSDevicePhaseOnline,
			RuntimeSeconds:       &runtimeSeconds,
			BatteryChargePercent: &charge,
			LastPollTime:         &polled,
			LastStatus:           "OL",
		},
	}
}

// The whole point of F-42: a poll that moves only the timestamp must not re-enqueue every flow.
// This is the shape that produced ~1 reconcile every 48 seconds in production.
func TestUPSDeviceWatchIgnoresATelemetryPollThatChangedNothingRelevant(t *testing.T) {
	oldDevice := triggerDevice()
	newDevice := triggerDevice()
	polled := metav1.NewTime(time.Date(2026, 8, 9, 20, 0, 15, 0, time.UTC))
	newDevice.Status.LastPollTime = &polled
	load := int32(42)
	newDevice.Status.LoadPercent = &load

	if upsDeviceTriggerInputsChanged(oldDevice, newDevice) {
		t.Fatal("a poll that only refreshed lastPollTime and loadPercent must not enqueue a reconcile")
	}
}

func TestUPSDeviceWatchAdmitsChangesTheTriggerLogicReads(t *testing.T) {
	lowerRuntime := int64(120)
	lowerCharge := int32(11)

	for name, mutate := range map[string]func(*powerv1alpha1.UPSDevice){
		"phase":         func(d *powerv1alpha1.UPSDevice) { d.Status.Phase = powerv1alpha1.UPSDevicePhaseOnBattery },
		"runtime":       func(d *powerv1alpha1.UPSDevice) { d.Status.RuntimeSeconds = &lowerRuntime },
		"charge":        func(d *powerv1alpha1.UPSDevice) { d.Status.BatteryChargePercent = &lowerCharge },
		"power domains": func(d *powerv1alpha1.UPSDevice) { d.Spec.PowerDomains = []string{"core", "rack-b"} },
		"generation":    func(d *powerv1alpha1.UPSDevice) { d.Generation = 2 },
		// Telemetry going absent has to reach the flow: PL-32 turns missing runtime into an
		// Unknown verdict, and it can only do that if the change is delivered.
		"runtime disappearing": func(d *powerv1alpha1.UPSDevice) { d.Status.RuntimeSeconds = nil },
		"charge disappearing":  func(d *powerv1alpha1.UPSDevice) { d.Status.BatteryChargePercent = nil },
	} {
		t.Run(name, func(t *testing.T) {
			oldDevice := triggerDevice()
			newDevice := triggerDevice()
			mutate(newDevice)
			if !upsDeviceTriggerInputsChanged(oldDevice, newDevice) {
				t.Fatalf("a change to %s must enqueue a reconcile", name)
			}
		})
	}
}

// A stale reading going from absent to present is as much a change as the reverse.
func TestUPSDeviceWatchAdmitsTelemetryAppearing(t *testing.T) {
	oldDevice := triggerDevice()
	oldDevice.Status.RuntimeSeconds = nil
	newDevice := triggerDevice()

	if !upsDeviceTriggerInputsChanged(oldDevice, newDevice) {
		t.Fatal("runtime telemetry appearing must enqueue a reconcile")
	}
}

// Creates, deletes, and objects the predicate cannot reason about are admitted. A missed enqueue is
// a correctness failure during an outage; a surplus one only costs work.
func TestUPSDeviceWatchAdmitsEverythingItCannotAssertAbout(t *testing.T) {
	p := upsDeviceTriggerRelevantPredicate()

	if !p.Create(event.CreateEvent{Object: triggerDevice()}) {
		t.Fatal("create must be admitted")
	}
	if !p.Delete(event.DeleteEvent{Object: triggerDevice()}) {
		t.Fatal("delete must be admitted")
	}
	if !p.Generic(event.GenericEvent{Object: triggerDevice()}) {
		t.Fatal("generic must be admitted")
	}
	if !p.Update(event.UpdateEvent{ObjectOld: &powerv1alpha1.NUTServer{}, ObjectNew: &powerv1alpha1.NUTServer{}}) {
		t.Fatal("an object this predicate cannot type-assert must be admitted rather than dropped")
	}
}

func TestUPSDeviceWatchPredicateRunsThroughTheUpdateFunc(t *testing.T) {
	p := upsDeviceTriggerRelevantPredicate()
	oldDevice := triggerDevice()
	quiet := triggerDevice()
	polled := metav1.NewTime(time.Date(2026, 8, 9, 20, 5, 0, 0, time.UTC))
	quiet.Status.LastPollTime = &polled

	if p.Update(event.UpdateEvent{ObjectOld: oldDevice, ObjectNew: quiet}) {
		t.Fatal("a poll-only update must be filtered by the predicate itself, not just the helper")
	}

	loud := triggerDevice()
	loud.Status.Phase = powerv1alpha1.UPSDevicePhaseLowBattery
	if !p.Update(event.UpdateEvent{ObjectOld: oldDevice, ObjectNew: loud}) {
		t.Fatal("a phase change must pass the predicate")
	}
}
