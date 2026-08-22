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
	"errors"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/metrics"
	"github.com/MichaelZalud18/nut-operator/internal/nut"
	"github.com/MichaelZalud18/nut-operator/internal/polling"
	storageconfig "github.com/MichaelZalud18/nut-operator/internal/storage"
	"github.com/MichaelZalud18/nut-operator/internal/telemetry"
)

const (
	defaultTelemetryPollInterval      = 30 * time.Second
	defaultTelemetryAlertPollInterval = 10 * time.Second
)

type telemetryPoller interface {
	Poll(ctx context.Context, target polling.Target) (polling.Result, error)
}

type telemetryAuditRecorder interface {
	RecordTelemetrySnapshot(ctx context.Context, clusterName string, result polling.Result) error
}

// UPSDeviceReconciler reconciles a UPSDevice object
type UPSDeviceReconciler struct {
	client.Client
	Scheme            *runtime.Scheme
	TelemetryPoller   telemetryPoller
	TelemetryRecorder telemetryAuditRecorder
	StorageConnector  storageconfig.AuditStoreConnector
}

// +kubebuilder:rbac:groups=power.zalud.io,resources=upsdevices,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=power.zalud.io,resources=upsdevices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=power.zalud.io,resources=upsdevices/finalizers,verbs=update
// +kubebuilder:rbac:groups=power.zalud.io,resources=nutservers,verbs=get;list;watch
// +kubebuilder:rbac:groups=power.zalud.io,resources=powermanagementclusters,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get
// This reconciler records events on nine paths and had no grant for any of them (F-115).
// Both groups: the recorder writes through events.k8s.io, while the legacy core group stays
// for any client still reading events there.
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=events.k8s.io,resources=events,verbs=create;patch

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
	base := device.DeepCopy()

	result := validateUPSDevice(&device)
	device.Status.ObservedGeneration = device.Generation
	device.Status.NUTName = nutDeviceName(device)

	// Which capability profile this device resolves to is a property of the device's identity, so
	// it is published whether or not a NUTServer is serving it yet and whether or not telemetry is
	// flowing. A device sitting unserved still has a profile, and that is exactly when someone is
	// looking at it.
	capabilityMatch, capabilityDiagnostics, capabilityErr := resolveDeviceCapabilityMatch(ctx, r.Client, &device)
	recordCapabilityMatchMetric(capabilityMatch, capabilityErr)
	device.Status.Capability = capabilityStatusFromMatch(capabilityMatch, capabilityDiagnostics, capabilityErr)
	reconcileResult := ctrl.Result{}
	var telemetryRecord polling.Result
	telemetryRecordCluster := ""
	shouldRecordTelemetry := false
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
			pollStart := time.Now()
			pollResult, pollErr := r.telemetryPoller().Poll(ctx, target)
			pollResultLabel := telemetryPollMetricResult(pollErr)
			metrics.UPSDeviceTelemetryPollsTotal.WithLabelValues(device.Name, pollResultLabel).Inc()
			metrics.UPSDeviceTelemetryPollDurationSeconds.WithLabelValues(device.Name, pollResultLabel).Observe(time.Since(pollStart).Seconds())
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
				applyIdentityStatus(&device, pollResult.Snapshot)
				telemetryRecord = pollResult
				telemetryRecordCluster = resolution.ManagementClusterName
				shouldRecordTelemetry = telemetryRecordCluster != ""
				metrics.UPSDeviceTelemetryLastSuccessTimestampSeconds.WithLabelValues(device.Name).Set(float64(pollResult.Snapshot.ObservedAt.Unix()))
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

	// Published on every path, not just the polling one. status.identity keeps the last reported
	// values when a poll fails, so the verdict stays answerable during an outage -- and a device
	// that has never polled says so through Unknown rather than through a missing condition.
	identity := evaluateIdentity(device.Status.Identity)
	setIdentityVerifiedCondition(&device.Status.Conditions, device.Generation, identity.status, identity.reason, identity.message)
	if identity.status == metav1.ConditionFalse {
		log.Info("Declared UPS model does not match the model the device reports",
			"upsdevice", device.Name,
			"declaredModel", device.Status.Identity.DeclaredModel,
			"reportedModel", device.Status.Identity.ReportedModel,
		)
	}

	if err := r.Status().Patch(ctx, &device, client.MergeFrom(base)); err != nil {
		log.Error(err, "failed to update UPSDevice status")
		return ctrl.Result{}, err
	}

	if shouldRecordTelemetry {
		if err := r.telemetryRecorder().RecordTelemetrySnapshot(ctx, telemetryRecordCluster, telemetryRecord); err != nil {
			log.Error(err, "failed to record UPSDevice telemetry audit snapshot", "upsdevice", device.Name, "powermanagementcluster", telemetryRecordCluster)
		}
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

func (r *UPSDeviceReconciler) telemetryRecorder() telemetryAuditRecorder {
	if r.TelemetryRecorder != nil {
		return r.TelemetryRecorder
	}
	return storageTelemetryRecorder{
		client:    r.Client,
		connector: r.storageConnector(),
	}
}

func (r *UPSDeviceReconciler) storageConnector() storageconfig.AuditStoreConnector {
	if r.StorageConnector != nil {
		return r.StorageConnector
	}
	return storageconfig.NewKubernetesConnector(r.Client, storageconfig.ConnectorOptions{})
}

type telemetryTargetResolution struct {
	Found                 bool
	Reason                string
	Message               string
	ServerRefs            []string
	ManagementClusterName string
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
	targetManagementClusterName := ""
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
			targetManagementClusterName = nutServerManagementClusterName(server)
			targetFound = true
		}
	}
	sort.Strings(resolution.ServerRefs)
	if targetFound {
		// The matched capability profile supplies variable aliases, so a device
		// reporting a non-standard name still produces canonical derived
		// fields. A matching failure must not take telemetry down with it: the
		// device still polls, just without alias resolution.
		//
		// Only the aliases are taken here. The resolution itself is published
		// from Reconcile, because it depends on the device's identity and not
		// on whether a NUTServer is serving it yet -- doing it here left every
		// device without a ready telemetry target reporting no profile at all.
		if match, _, err := resolveDeviceCapabilityMatch(ctx, r.Client, device); err == nil {
			target.TelemetryAliases = telemetryAliasesFromMatch(match)
		}
		return target, telemetryTargetResolution{
			Found:                 true,
			Reason:                "TelemetryTargetResolved",
			Message:               fmt.Sprintf("NUTServer %q service endpoint is ready for telemetry polling", target.NUTServer),
			ServerRefs:            resolution.ServerRefs,
			ManagementClusterName: targetManagementClusterName,
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

func nutServerManagementClusterName(server *powerv1alpha1.NUTServer) string {
	if server == nil || server.Spec.ManagementClusterRef == nil {
		return ""
	}
	return server.Spec.ManagementClusterRef.Name
}

func nutServerSelectsDevice(server *powerv1alpha1.NUTServer, device *powerv1alpha1.UPSDevice) (bool, error) {
	if len(server.Status.SelectedDevices) > 0 && server.Status.ObservedGeneration == server.Generation {
		return stringSliceContains(server.Status.SelectedDevices, device.Name), nil
	}
	return nutServerSpecSelectsDevice(server, device)
}

// nutServerSpecSelectsDevice answers the selection question from the spec alone,
// ignoring whatever the last reconcile recorded.
//
// Split out because the two callers need different things. Reconcile prefers the
// server's own record of what it selected, which is authoritative once the
// generation matches. Watch mapping cannot: it is asked about the old and the new
// object in turn, and a device just relabelled into a selector is absent from that
// record by definition, so consulting it would drop the arrival half of the move.
func nutServerSpecSelectsDevice(server *powerv1alpha1.NUTServer, device *powerv1alpha1.UPSDevice) (bool, error) {
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

func telemetryPollMetricResult(err error) string {
	if err != nil {
		return "Failed"
	}
	return "Succeeded"
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

type storageTelemetryRecorder struct {
	client    client.Client
	connector storageconfig.AuditStoreConnector
}

func (r storageTelemetryRecorder) RecordTelemetrySnapshot(ctx context.Context, clusterName string, result polling.Result) error {
	if clusterName == "" {
		return nil
	}
	if r.client == nil {
		return errors.New("kubernetes client is required to record telemetry audit snapshots")
	}
	var cluster powerv1alpha1.PowerManagementCluster
	if err := r.client.Get(ctx, types.NamespacedName{Name: clusterName}, &cluster); err != nil {
		return fmt.Errorf("get PowerManagementCluster %q for telemetry audit: %w", clusterName, err)
	}
	if !managementClusterStorageReady(&cluster) {
		return nil
	}
	connector := r.connector
	if connector == nil {
		connector = storageconfig.NewKubernetesConnector(r.client, storageconfig.ConnectorOptions{})
	}
	store, err := connector.OpenAuditStore(ctx, &cluster)
	if err != nil {
		return err
	}
	recordErr := store.RecordTelemetrySnapshot(ctx, result.AuditSnapshot(uuid.NewString()))
	closeErr := store.Close()
	if recordErr != nil || closeErr != nil {
		return errors.Join(recordErr, closeErr)
	}
	return nil
}
