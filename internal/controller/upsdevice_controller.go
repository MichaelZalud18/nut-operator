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
	"fmt"
	"math"
	"sort"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/nut"
	"github.com/MichaelZalud18/nut-operator/internal/polling"
	"github.com/MichaelZalud18/nut-operator/internal/telemetry"
)

const (
	defaultTelemetryPollInterval      = 30 * time.Second
	defaultTelemetryAlertPollInterval = 10 * time.Second
)

type telemetryPoller interface {
	Poll(ctx context.Context, target polling.Target) (polling.Result, error)
}

// UPSDeviceReconciler reconciles a UPSDevice object
type UPSDeviceReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	TelemetryPoller telemetryPoller
}

// +kubebuilder:rbac:groups=power.zalud.io,resources=upsdevices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=power.zalud.io,resources=upsdevices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=power.zalud.io,resources=upsdevices/finalizers,verbs=update
// +kubebuilder:rbac:groups=power.zalud.io,resources=nutservers,verbs=get;list;watch

// Reconcile validates UPS device configuration and records telemetry readiness status.
func (r *UPSDeviceReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var device powerv1alpha1.UPSDevice
	if err := r.Get(ctx, req.NamespacedName, &device); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	result := validateUPSDevice(&device)
	device.Status.ObservedGeneration = device.Generation
	device.Status.NUTName = nutDeviceName(device)
	reconcileResult := ctrl.Result{}
	if !result.accepted {
		device.Status.Phase = powerv1alpha1.UPSDevicePhaseUnavailable
		device.Status.ServerRefs = nil
		setAcceptedCondition(&device.Status.Conditions, device.Generation, result)
		setReadyCondition(
			&device.Status.Conditions,
			device.Generation,
			false,
			"ValidationFailed",
			result.message,
		)
		setDegradedCondition(&device.Status.Conditions, device.Generation, true, result.reason, result.message)
	} else {
		target, resolution, err := r.resolveTelemetryTarget(ctx, &device)
		if err != nil {
			return ctrl.Result{}, err
		}
		device.Status.ServerRefs = resolution.ServerRefs
		setAcceptedCondition(&device.Status.Conditions, device.Generation, result)
		if !resolution.Found {
			device.Status.Phase = powerv1alpha1.UPSDevicePhaseUnknown
			setReadyCondition(
				&device.Status.Conditions,
				device.Generation,
				false,
				resolution.Reason,
				resolution.Message,
			)
			setDegradedCondition(&device.Status.Conditions, device.Generation, false, "NotDegraded", "telemetry target is not ready yet")
		} else {
			pollResult, pollErr := r.telemetryPoller().Poll(ctx, target)
			if pollErr != nil {
				device.Status.Phase = failedTelemetryPhase(device.Status.LastPollTime)
				reconcileResult = ctrl.Result{RequeueAfter: telemetryPollInterval(&device, true)}
				setReadyCondition(
					&device.Status.Conditions,
					device.Generation,
					false,
					"TelemetryPollFailed",
					pollErr.Error(),
				)
				setDegradedCondition(&device.Status.Conditions, device.Generation, true, "TelemetryPollFailed", pollErr.Error())
			} else {
				applyTelemetrySnapshotStatus(&device, pollResult.Snapshot)
				reconcileResult = ctrl.Result{RequeueAfter: telemetryPollIntervalForPhase(&device, pollResult.Snapshot.Phase)}
				degraded, reason, message := telemetryDegradedStatus(pollResult.Snapshot)
				setReadyCondition(
					&device.Status.Conditions,
					device.Generation,
					true,
					"TelemetryAvailable",
					"live NUT telemetry is available",
				)
				setDegradedCondition(&device.Status.Conditions, device.Generation, degraded, reason, message)
			}
		}
	}

	if err := r.Status().Update(ctx, &device); err != nil {
		log.Error(err, "failed to update UPSDevice status")
		return ctrl.Result{}, err
	}

	return reconcileResult, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *UPSDeviceReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&powerv1alpha1.UPSDevice{}).
		Watches(&powerv1alpha1.NUTServer{}, handler.EnqueueRequestsFromMapFunc(r.upsDeviceRequestsForNUTServer)).
		Named("upsdevice").
		Complete(r)
}

func (r *UPSDeviceReconciler) telemetryPoller() telemetryPoller {
	if r.TelemetryPoller != nil {
		return r.TelemetryPoller
	}
	return polling.NewPoller(polling.Options{})
}

type telemetryTargetResolution struct {
	Found      bool
	Reason     string
	Message    string
	ServerRefs []string
}

func (r *UPSDeviceReconciler) resolveTelemetryTarget(ctx context.Context, device *powerv1alpha1.UPSDevice) (polling.Target, telemetryTargetResolution, error) {
	var servers powerv1alpha1.NUTServerList
	if err := r.List(ctx, &servers); err != nil {
		return polling.Target{}, telemetryTargetResolution{}, fmt.Errorf("list NUTServers for UPSDevice %q telemetry: %w", device.Name, err)
	}
	sort.Slice(servers.Items, func(i, j int) bool {
		return servers.Items[i].Name < servers.Items[j].Name
	})

	resolution := telemetryTargetResolution{
		Reason:  "NUTServerSelectionPending",
		Message: "no NUTServer has selected this UPSDevice yet",
	}
	var target polling.Target
	targetFound := false
	endpointMissingServer := ""
	for i := range servers.Items {
		server := &servers.Items[i]
		selected, err := nutServerSelectsDevice(server, device)
		if err != nil {
			return polling.Target{}, telemetryTargetResolution{}, err
		}
		if !selected {
			continue
		}
		resolution.ServerRefs = append(resolution.ServerRefs, server.Name)
		if !nutServerTelemetryReady(server) {
			if resolution.Reason == "NUTServerSelectionPending" {
				resolution.Reason = "NUTServerNotReady"
				resolution.Message = fmt.Sprintf("NUTServer %q selected this UPSDevice but is not ready", server.Name)
			}
			continue
		}
		endpoint, ok := firstTelemetryEndpoint(server)
		if !ok {
			if endpointMissingServer == "" {
				endpointMissingServer = server.Name
			}
			continue
		}
		if !targetFound {
			target = polling.Target{
				UPSDevice: device.Name,
				NUTServer: server.Name,
				NUTName:   nutDeviceName(*device),
				Host:      endpoint.Host,
				Port:      endpoint.Port,
			}
			targetFound = true
		}
	}
	sort.Strings(resolution.ServerRefs)
	if targetFound {
		return target, telemetryTargetResolution{
			Found:      true,
			Reason:     "TelemetryTargetResolved",
			Message:    fmt.Sprintf("NUTServer %q service endpoint is ready for telemetry polling", target.NUTServer),
			ServerRefs: resolution.ServerRefs,
		}, nil
	}
	if endpointMissingServer != "" {
		return polling.Target{}, telemetryTargetResolution{
			Reason:     "NUTServerEndpointMissing",
			Message:    fmt.Sprintf("NUTServer %q is ready but does not report a service endpoint", endpointMissingServer),
			ServerRefs: resolution.ServerRefs,
		}, nil
	}
	return polling.Target{}, resolution, nil
}

type telemetryEndpoint struct {
	Host string
	Port int
}

func firstTelemetryEndpoint(server *powerv1alpha1.NUTServer) (telemetryEndpoint, bool) {
	for _, endpoint := range server.Status.ServiceEndpoints {
		host := endpoint.DNSName
		if host == "" && endpoint.Name != "" && endpoint.Namespace != "" {
			host = fmt.Sprintf("%s.%s.svc.cluster.local", endpoint.Name, endpoint.Namespace)
		}
		if host == "" {
			continue
		}
		port := int(endpoint.Port)
		if port == 0 {
			port = nut.DefaultPort
		}
		return telemetryEndpoint{Host: host, Port: port}, true
	}
	return telemetryEndpoint{}, false
}

func nutServerSelectsDevice(server *powerv1alpha1.NUTServer, device *powerv1alpha1.UPSDevice) (bool, error) {
	if len(server.Status.SelectedDevices) > 0 && server.Status.ObservedGeneration == server.Generation {
		return stringSliceContains(server.Status.SelectedDevices, device.Name), nil
	}
	for _, ref := range server.Spec.DeviceRefs {
		if ref.Name == device.Name {
			return true, nil
		}
	}
	if server.Spec.DeviceSelector == nil {
		return false, nil
	}
	selector, err := metav1.LabelSelectorAsSelector(server.Spec.DeviceSelector)
	if err != nil {
		return false, fmt.Errorf("parse NUTServer %q deviceSelector: %w", server.Name, err)
	}
	return selector.Matches(labels.Set(device.Labels)), nil
}

func nutServerTelemetryReady(server *powerv1alpha1.NUTServer) bool {
	if server.Status.ObservedGeneration != server.Generation {
		return false
	}
	if server.Status.Phase != powerv1alpha1.NUTServerPhaseReady {
		return false
	}
	condition := meta.FindStatusCondition(server.Status.Conditions, powerv1alpha1.ConditionReady)
	return condition != nil && condition.Status == metav1.ConditionTrue
}

func applyTelemetrySnapshotStatus(device *powerv1alpha1.UPSDevice, snapshot telemetry.Snapshot) {
	device.Status.Phase = upsDevicePhase(snapshot.Phase)
	pollTime := metav1.NewTime(snapshot.ObservedAt.UTC())
	device.Status.LastPollTime = &pollTime
	device.Status.LastStatus = snapshot.UPSStatus
	device.Status.BatteryChargePercent = int32FromFloat(snapshot.BatteryChargePercent)
	device.Status.RuntimeSeconds = snapshot.RuntimeSeconds
	device.Status.LoadPercent = int32FromFloat(snapshot.LoadPercent)
}

func upsDevicePhase(phase string) powerv1alpha1.UPSDevicePhase {
	switch phase {
	case telemetry.PhaseOnline:
		return powerv1alpha1.UPSDevicePhaseOnline
	case telemetry.PhaseOnBattery:
		return powerv1alpha1.UPSDevicePhaseOnBattery
	case telemetry.PhaseLowBattery:
		return powerv1alpha1.UPSDevicePhaseLowBattery
	case telemetry.PhaseStale:
		return powerv1alpha1.UPSDevicePhaseStale
	case telemetry.PhaseUnavailable:
		return powerv1alpha1.UPSDevicePhaseUnavailable
	default:
		return powerv1alpha1.UPSDevicePhaseUnknown
	}
}

func int32FromFloat(value *float64) *int32 {
	if value == nil {
		return nil
	}
	roundedFloat := math.Round(*value)
	const (
		minInt32Float = float64(-1 << 31)
		maxInt32Float = float64(1<<31 - 1)
	)
	if math.IsNaN(roundedFloat) || math.IsInf(roundedFloat, 0) || roundedFloat < minInt32Float || roundedFloat > maxInt32Float {
		return nil
	}
	rounded := int32(roundedFloat)
	return &rounded
}

func failedTelemetryPhase(lastPollTime *metav1.Time) powerv1alpha1.UPSDevicePhase {
	if lastPollTime != nil {
		return powerv1alpha1.UPSDevicePhaseStale
	}
	return powerv1alpha1.UPSDevicePhaseUnavailable
}

func telemetryDegradedStatus(snapshot telemetry.Snapshot) (bool, string, string) {
	if len(snapshot.Diagnostics) > 0 {
		diagnostic := snapshot.Diagnostics[0]
		return true, diagnostic.Reason, diagnostic.Message
	}
	switch snapshot.Phase {
	case telemetry.PhaseOnline:
		return false, "NotDegraded", "UPS telemetry is online"
	case telemetry.PhaseOnBattery:
		return true, "UPSOnBattery", "UPS is running on battery"
	case telemetry.PhaseLowBattery:
		return true, "UPSLowBattery", "UPS is reporting low battery or forced shutdown"
	case telemetry.PhaseStale:
		return true, "UPSStatusStale", "UPS status is stale or communication is unavailable"
	case telemetry.PhaseUnavailable:
		return true, "UPSUnavailable", "UPS is unavailable"
	default:
		return true, "UPSStatusUnknown", "UPS telemetry status could not be classified"
	}
}

func telemetryPollIntervalForPhase(device *powerv1alpha1.UPSDevice, phase string) time.Duration {
	degraded := phase != telemetry.PhaseOnline
	return telemetryPollInterval(device, degraded)
}

func telemetryPollInterval(device *powerv1alpha1.UPSDevice, degraded bool) time.Duration {
	if degraded && device.Spec.Telemetry.AlertPollInterval != nil && device.Spec.Telemetry.AlertPollInterval.Duration > 0 {
		return device.Spec.Telemetry.AlertPollInterval.Duration
	}
	if device.Spec.Telemetry.PollInterval != nil && device.Spec.Telemetry.PollInterval.Duration > 0 {
		return device.Spec.Telemetry.PollInterval.Duration
	}
	if degraded {
		return defaultTelemetryAlertPollInterval
	}
	return defaultTelemetryPollInterval
}

func (r *UPSDeviceReconciler) upsDeviceRequestsForNUTServer(ctx context.Context, obj client.Object) []reconcile.Request {
	server, ok := obj.(*powerv1alpha1.NUTServer)
	if !ok {
		return nil
	}
	names, err := r.upsDeviceNamesForNUTServer(ctx, server)
	if err != nil {
		logf.FromContext(ctx).Error(err, "failed to map NUTServer change to UPSDevice requests", "nutserver", server.Name)
		return nil
	}
	requests := make([]reconcile.Request, 0, len(names))
	for _, name := range names {
		requests = append(requests, reconcile.Request{NamespacedName: client.ObjectKey{Name: name}})
	}
	return requests
}

func (r *UPSDeviceReconciler) upsDeviceNamesForNUTServer(ctx context.Context, server *powerv1alpha1.NUTServer) ([]string, error) {
	names := map[string]struct{}{}
	for _, name := range server.Status.SelectedDevices {
		names[name] = struct{}{}
	}
	for _, ref := range server.Spec.DeviceRefs {
		names[ref.Name] = struct{}{}
	}
	if server.Spec.DeviceSelector != nil {
		selector, err := metav1.LabelSelectorAsSelector(server.Spec.DeviceSelector)
		if err != nil {
			return nil, err
		}
		var devices powerv1alpha1.UPSDeviceList
		if err := r.List(ctx, &devices); err != nil {
			return nil, err
		}
		for _, device := range devices.Items {
			if selector.Matches(labels.Set(device.Labels)) {
				names[device.Name] = struct{}{}
			}
		}
	}

	sorted := make([]string, 0, len(names))
	for name := range names {
		if name != "" {
			sorted = append(sorted, name)
		}
	}
	sort.Strings(sorted)
	return sorted, nil
}

func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
