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
	"strings"
	"time"

	"github.com/google/uuid"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	powerv1alpha1 "github.com/MichaelZalud18/nut-operator/api/v1alpha1"
	"github.com/MichaelZalud18/nut-operator/internal/audit"
	"github.com/MichaelZalud18/nut-operator/internal/capability"
	"github.com/MichaelZalud18/nut-operator/internal/polling"
	storageconfig "github.com/MichaelZalud18/nut-operator/internal/storage"
)

// probeRetryInterval backs off a probe that could not reach its device. A UPS
// that is unreachable right now is an ordinary condition to report, not a
// reason to retry at controller speed.
const probeRetryInterval = 2 * time.Minute

// UPSCapabilityProbeReconciler reconciles a UPSCapabilityProbe object.
//
// This is the "my UPS has no packaged profile" helper. It reads what a device
// actually reports and drafts a profile from it. Per RS-7 through RS-10 probing
// is advisory reconciliation work: the result lands in status and nowhere else.
// It never changes how a device resolves, never reaches the planner, and never
// runs on the failure path.
type UPSCapabilityProbeReconciler struct {
	client.Client
	Scheme          *runtime.Scheme
	TelemetryPoller telemetryPoller
	Clock           func() time.Time
	// StorageConnector opens the audit store that receives probe-history rows
	// (OD-15). Optional: with no PostgreSQL configured the probe still drafts a
	// profile, it just keeps no history.
	StorageConnector storageconfig.AuditStoreConnector
}

// +kubebuilder:rbac:groups=power.zalud.io,resources=upscapabilityprobes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=power.zalud.io,resources=upscapabilityprobes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=power.zalud.io,resources=upscapabilityprobes/finalizers,verbs=update
// This reconciler reads UPSDevice, NUTServer, and UPSCapabilityProfile to
// resolve a poll target and report the device's current match. Those grants are
// declared here rather than inherited from whichever other controller happens
// to request them: the generated ClusterRole is the union of every reconciler's
// markers, so an unstated dependency survives only until someone narrows the
// controller it was borrowing from.
// +kubebuilder:rbac:groups=power.zalud.io,resources=upsdevices;nutservers;upscapabilityprofiles,verbs=get;list;watch

// Reconcile probes the referenced UPSDevice and drafts a capability profile.
func (r *UPSCapabilityProbeReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var probe powerv1alpha1.UPSCapabilityProbe
	if err := r.Get(ctx, req.NamespacedName, &probe); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}
	base := probe.DeepCopy()

	probe.Status.ObservedGeneration = probe.Generation
	requeue, err := r.runProbe(ctx, &probe)

	if patchErr := r.Status().Patch(ctx, &probe, client.MergeFrom(base)); patchErr != nil {
		log.Error(patchErr, "Could not update UPSCapabilityProbe status")
		return ctrl.Result{}, patchErr
	}
	if err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// runProbe fills in probe status and returns how long to wait before probing
// again. A device that cannot be read is recorded as a probe failure rather
// than returned as a reconcile error.
func (r *UPSCapabilityProbeReconciler) runProbe(ctx context.Context, probe *powerv1alpha1.UPSCapabilityProbe) (time.Duration, error) {
	var device powerv1alpha1.UPSDevice
	if err := r.Get(ctx, client.ObjectKey{Name: probe.Spec.DeviceRef.Name}, &device); err != nil {
		if apierrors.IsNotFound(err) {
			r.failProbe(probe, "UPSDeviceNotFound",
				fmt.Sprintf("UPSDevice %q does not exist", probe.Spec.DeviceRef.Name))
			return 0, nil
		}
		return 0, err
	}

	deviceReconciler := &UPSDeviceReconciler{Client: r.Client, Scheme: r.Scheme}
	target, resolution, err := deviceReconciler.resolveTelemetryTarget(ctx, &device)
	if err != nil {
		return 0, err
	}
	if !resolution.Found {
		r.failProbe(probe, resolution.Reason, resolution.Message)
		return probeRetryInterval, nil
	}

	pollResult, err := r.telemetryPoller().Poll(ctx, target)
	if err != nil {
		r.failProbe(probe, "ProbeReadFailed",
			fmt.Sprintf("could not read NUT variables for UPSDevice %q: %v", device.Name, err))
		return probeRetryInterval, nil
	}

	// Resolved once and shared: status reports which profile the device matches
	// today, and the verification record compares that same profile against
	// this same read. Resolving twice could report a match in status that the
	// verification row disagrees with.
	match, matchErr := resolveDeviceCapabilityMatch(ctx, r.Client, &device)

	r.recordDraft(probe, &device, pollResult, match, matchErr)
	r.recordVerification(ctx, probe, &device, pollResult, match, matchErr, resolution.ManagementClusterName)

	if probe.Spec.RefreshInterval != nil && probe.Spec.RefreshInterval.Duration > 0 {
		return probe.Spec.RefreshInterval.Duration, nil
	}
	return 0, nil
}

// recordVerification persists probe history to PostgreSQL (OD-15): what the
// device reported, which profile version it matched, and whether that profile's
// declared telemetry still holds. History belongs in PostgreSQL rather than CR
// status because "last verified against firmware X" is a series, and status is
// a single current summary that each reconcile overwrites.
//
// Recording is best-effort by design. A probe is advisory work (RS-7–RS-10)
// that never runs on the failure path, so an unreachable database must not cost
// the operator a drafted profile. It is not silent either: a write that was
// attempted and failed lands on the Degraded condition.
func (r *UPSCapabilityProbeReconciler) recordVerification(
	ctx context.Context,
	probe *powerv1alpha1.UPSCapabilityProbe,
	device *powerv1alpha1.UPSDevice,
	pollResult polling.Result,
	match capability.MatchResult,
	matchErr error,
	clusterName string,
) {
	if matchErr != nil {
		// Without a resolved match there is no profile version to tie evidence
		// to, and a row claiming an empty profile would be worse than no row.
		setDegradedCondition(&probe.Status.Conditions, probe.Generation, true, "CapabilityMatchFailed",
			fmt.Sprintf("could not resolve the current capability profile for UPSDevice %q: %v", device.Name, matchErr))
		return
	}

	verification := capability.Verify(match, pollResult.Snapshot.Variables)

	// Drift is a live fact about the device in front of us, so it belongs on the
	// condition regardless of whether the history write succeeds. A user without
	// PostgreSQL still needs to know their profile stopped matching reality.
	if verification.DriftDetected {
		setDegradedCondition(&probe.Status.Conditions, probe.Generation, true, "CapabilityProfileDrift",
			fmt.Sprintf("profile %q declares telemetry UPSDevice %q did not report: %v",
				match.ProfileID, device.Name, verification.MissingVariables))
	}

	if err := r.writeVerification(ctx, device, pollResult, match, verification, clusterName); err != nil {
		setDegradedCondition(&probe.Status.Conditions, probe.Generation, true, "VerificationNotRecorded",
			fmt.Sprintf("probed UPSDevice %q but could not record probe history: %v", device.Name, err))
	}
}

// writeVerification opens the audit store and writes one row. It returns nil
// when there is nothing to write to: no management cluster resolved, or storage
// not ready. Those are ordinary configurations, not failures.
func (r *UPSCapabilityProbeReconciler) writeVerification(
	ctx context.Context,
	device *powerv1alpha1.UPSDevice,
	pollResult polling.Result,
	match capability.MatchResult,
	verification capability.Verification,
	clusterName string,
) error {
	if clusterName == "" {
		return nil
	}
	var cluster powerv1alpha1.PowerManagementCluster
	if err := r.Get(ctx, types.NamespacedName{Name: clusterName}, &cluster); err != nil {
		return fmt.Errorf("get PowerManagementCluster %q for probe history: %w", clusterName, err)
	}
	if !managementClusterStorageReady(&cluster) {
		return nil
	}

	connector := r.StorageConnector
	if connector == nil {
		connector = storageconfig.NewKubernetesConnector(r.Client, storageconfig.ConnectorOptions{})
	}
	store, err := connector.OpenAuditStore(ctx, &cluster)
	if err != nil {
		return err
	}

	recordErr := store.RecordCapabilityProfileVerification(ctx, audit.CapabilityProfileVerification{
		VerificationID:      uuid.NewString(),
		ObservedAt:          r.now(),
		UPSDevice:           device.Name,
		ProfileID:           match.ProfileID,
		ProfileVersion:      match.ProfileVersion,
		ProfileSource:       string(match.ProfileSource),
		Model:               strings.TrimSpace(pollResult.Snapshot.Variables[capability.VariableUPSModel]),
		Firmware:            strings.TrimSpace(pollResult.Snapshot.Variables[capability.VariableUPSFirmware]),
		NUTDriver:           renderedUPSDriver(*device),
		NUTServer:           pollResult.Target.NUTServer,
		NUTName:             pollResult.Target.NUTName,
		Verified:            verification.Verified,
		DriftDetected:       verification.DriftDetected,
		ProbeVariables:      pollResult.Snapshot.Variables,
		ExpectedVariables:   verification.ExpectedVariables,
		MissingVariables:    verification.MissingVariables,
		UnexpectedVariables: verification.UnexpectedVariables,
		Diagnostics:         auditDiagnosticsFromVerification(verification),
		Details: map[string]any{
			"matchTier":    string(match.Tier),
			"unidentified": match.Unidentified,
			"profileHash":  match.ProfileHash,
		},
	})
	closeErr := store.Close()
	return errors.Join(recordErr, closeErr)
}

func auditDiagnosticsFromVerification(verification capability.Verification) []audit.DiagnosticRecord {
	if len(verification.Diagnostics) == 0 {
		return nil
	}
	records := make([]audit.DiagnosticRecord, 0, len(verification.Diagnostics))
	for _, diagnostic := range verification.Diagnostics {
		records = append(records, audit.DiagnosticRecord{
			Severity: diagnostic.Severity,
			Source:   "capability",
			Reason:   diagnostic.Code,
			Message:  diagnostic.Message,
		})
	}
	return records
}

func (r *UPSCapabilityProbeReconciler) recordDraft(
	probe *powerv1alpha1.UPSCapabilityProbe,
	device *powerv1alpha1.UPSDevice,
	pollResult polling.Result,
	match capability.MatchResult,
	matchErr error,
) {
	draft := capability.BuildDraft(capability.ProbeObservation{
		DeviceID:  device.Name,
		Variables: pollResult.Snapshot.Variables,
	}, capability.DraftOptions{ProfileName: probe.Spec.ProfileName})

	now := metav1.NewTime(r.now())
	probe.Status.Phase = powerv1alpha1.UPSCapabilityProbePhaseProbed
	probe.Status.ProbedAt = &now
	probe.Status.Identity = powerv1alpha1.UPSCapabilityProbeIdentity{
		Manufacturer: draft.Manufacturer,
		Model:        draft.Model,
		Firmware:     draft.Firmware,
	}
	probe.Status.ReportedVariables = draft.Variables
	probe.Status.NonStandardVariables = draft.NonStandardVariables
	probe.Status.SuggestedAliases = copyStringMap(draft.SuggestedAliases)
	probe.Status.DraftProfile = draft.ProfileYAML()
	probe.Status.IssueReport = draft.IssueReport()
	probe.Status.CurrentMatch = currentMatchStatus(match, matchErr)

	message := fmt.Sprintf("read %d NUT variables from UPSDevice %q; review status.draftProfile before applying it",
		len(draft.Variables), device.Name)
	setReadyCondition(&probe.Status.Conditions, probe.Generation, true, "Probed", message)
	setDegradedCondition(&probe.Status.Conditions, probe.Generation, false, "NotDegraded", message)
}

// currentMatchStatus records what the device resolves to today, so the gap the
// probe is filling is visible without a second lookup. A match failure is not
// worth failing the probe over: the draft is still useful without it, and the
// failure is reported on the Degraded condition by recordVerification.
func currentMatchStatus(match capability.MatchResult, matchErr error) powerv1alpha1.UPSCapabilityProbeMatch {
	if matchErr != nil {
		return powerv1alpha1.UPSCapabilityProbeMatch{}
	}
	return powerv1alpha1.UPSCapabilityProbeMatch{
		ProfileID:    match.ProfileID,
		Tier:         string(match.Tier),
		Unidentified: match.Unidentified,
	}
}

func (r *UPSCapabilityProbeReconciler) failProbe(probe *powerv1alpha1.UPSCapabilityProbe, reason, message string) {
	probe.Status.Phase = powerv1alpha1.UPSCapabilityProbePhaseFailed
	setReadyCondition(&probe.Status.Conditions, probe.Generation, false, reason, message)
	setDegradedCondition(&probe.Status.Conditions, probe.Generation, true, reason, message)
}

func (r *UPSCapabilityProbeReconciler) telemetryPoller() telemetryPoller {
	if r.TelemetryPoller != nil {
		return r.TelemetryPoller
	}
	return polling.NewPoller(polling.Options{})
}

func (r *UPSCapabilityProbeReconciler) now() time.Time {
	if r.Clock != nil {
		return r.Clock().UTC()
	}
	return time.Now().UTC()
}

// SetupWithManager sets up the controller with the Manager.
func (r *UPSCapabilityProbeReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&powerv1alpha1.UPSCapabilityProbe{}).
		Named("upscapabilityprobe").
		Complete(r)
}
