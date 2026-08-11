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

package executor

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MichaelZalud18/nut-operator/internal/adaptive"
)

func tierOf(tier int32) *int32 { return &tier }

func runtimeOf(seconds int64) *int64 { return &seconds }

func onBattery(seconds int64) adaptive.PowerObservation {
	return adaptive.PowerObservation{OnBattery: true, RuntimeSeconds: runtimeOf(seconds), RuntimeTrusted: true}
}

func onMains() adaptive.PowerObservation {
	return adaptive.PowerObservation{RuntimeSeconds: runtimeOf(3600), RuntimeTrusted: true}
}

// tieredInput is a three-wave flow descending tiers 4, 3, 2.
func tieredInput(observation adaptive.PowerObservation) Input {
	return Input{
		ExecutionID:    "execution-adaptive",
		ShutdownFlow:   "conserve-power",
		Mode:           ModeDryRun,
		PlanConfigHash: "plan-hash-a",
		Waves: []Wave{
			{Index: 0, ShutdownTier: tierOf(4), Groups: []string{"applications"}},
			{Index: 1, ShutdownTier: tierOf(3), Groups: []string{"databases"}},
			{Index: 2, ShutdownTier: tierOf(2), Groups: []string{"storage"}},
		},
		Groups: []Group{
			{Name: "applications", Action: "ScaleWorkload"},
			{Name: "databases", Action: "ScaleWorkload"},
			{Name: "storage", Action: "ScaleWorkload"},
		},
		Adaptive: AdaptiveInput{Observation: observation, FinalTier: 1},
	}
}

func newExecutor(writer *fakeAuditWriter) Executor {
	fixed := time.Date(2026, 8, 10, 23, 0, 0, 0, time.UTC)
	return Executor{
		Writer: writer,
		Clock:  func() time.Time { return fixed },
		NewID:  sequenceIDs(),
	}
}

// The pointer follows the waves down the tier sequence, and the run reports where it ended.
func TestPointerDescendsThroughTheWaveTiers(t *testing.T) {
	writer := &fakeAuditWriter{}

	result, err := newExecutor(writer).Execute(context.Background(), tieredInput(onBattery(1200)))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Phase != PhaseCompleted {
		t.Fatalf("phase = %q, want Completed", result.Phase)
	}
	if result.Adaptive.Pointer.Tier != 2 || result.Adaptive.Pointer.Deepest != 2 {
		t.Fatalf("pointer = %#v, want tier 2 deepest 2", result.Adaptive.Pointer)
	}
	if !result.Adaptive.Pointer.Started {
		t.Fatal("a flow that executed waves must report the pointer as started")
	}
}

// Tier 0 is last-ditch and excluded from flow targeting (OD-4). An unset limit must not be read
// as permission to reach it, and neither must a wave that names it.
func TestThePointerNeverReachesTheLastDitchTier(t *testing.T) {
	writer := &fakeAuditWriter{}
	input := tieredInput(onBattery(120))
	input.Waves = []Wave{
		{Index: 0, ShutdownTier: tierOf(2), Groups: []string{"applications"}},
		{Index: 1, ShutdownTier: tierOf(0), Groups: []string{"databases"}},
	}
	input.Groups = input.Groups[:2]
	// Deliberately unset, so the default rather than an explicit value is under test.
	input.Adaptive.FinalTier = 0

	result, err := newExecutor(writer).Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Adaptive.Pointer.Tier < 1 {
		t.Fatalf("pointer reached tier %d; tier 0 is last-ditch and unreachable by a flow",
			result.Adaptive.Pointer.Tier)
	}
}

// AE-1: on recovery the operator stops descending and restores nothing. Suspended is not
// Completed -- collapsing them would make "the outage ended" read as "the cluster shut down".
func TestPowerRecoverySuspendsTheFlowWithoutRestoringAnything(t *testing.T) {
	writer := &fakeAuditWriter{}
	input := tieredInput(onBattery(200))
	input.Adaptive.SuspendOnRecovery = true
	input.Adaptive.StartTier = 4

	observations := []adaptive.PowerObservation{onBattery(200), onMains(), onMains()}
	read := 0
	executor := newExecutor(writer)
	executor.Observer = func(context.Context) (adaptive.PowerObservation, error) {
		observation := observations[read]
		read++
		return observation, nil
	}

	result, err := executor.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Phase != PhaseSuspended || !result.Adaptive.Suspended {
		t.Fatalf("result = %#v, want a suspended flow", result)
	}
	// The first wave ran; the second and third did not.
	if result.Groups != 1 {
		t.Fatalf("groups executed = %d, want only the pre-recovery wave", result.Groups)
	}
	if phases := executionPhases(writer.executions); fmt.Sprint(phases) != "[Running Suspended]" {
		t.Fatalf("execution phases = %v, want [Running Suspended]", phases)
	}
	// AE-3: ascent is bookkeeping. Nothing is un-scaled, un-cordoned, or restarted.
	if len(writer.nodeReleases) != 0 {
		t.Fatalf("a suspension must not release nodes, got %#v", writer.nodeReleases)
	}
	if writer.executions[1].Reason != "PowerRecovered" {
		t.Fatalf("suspension reason = %q, want PowerRecovered", writer.executions[1].Reason)
	}
}

// AE-6 reserves halt for abort. A suspension must leave the pointer unlatched, or a second dip
// could never descend again -- which is the exact case the tier pointer exists to handle.
func TestSuspensionDoesNotLatchThePointer(t *testing.T) {
	writer := &fakeAuditWriter{}
	input := tieredInput(onBattery(200))
	input.Adaptive.SuspendOnRecovery = true
	input.Adaptive.StartTier = 4

	executor := newExecutor(writer)
	executor.Observer = func(context.Context) (adaptive.PowerObservation, error) {
		return onMains(), nil
	}

	result, err := executor.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Adaptive.Pointer.Halted {
		t.Fatal("power recovery must not latch the pointer; halt is reserved for abort (AE-6)")
	}
}

// The whole point of leaving the pointer where it stopped: a second dip descends from that depth
// and re-attempts the tiers it already ran as no-ops (AE-2), reported as re-execution.
func TestASecondDipResumesFromTheSuspendedDepth(t *testing.T) {
	first := &fakeAuditWriter{}
	firstInput := tieredInput(onBattery(200))
	firstInput.Adaptive.SuspendOnRecovery = true
	firstInput.Adaptive.StartTier = 4

	observations := []adaptive.PowerObservation{onBattery(200), onBattery(200), onMains()}
	read := 0
	executor := newExecutor(first)
	executor.Observer = func(context.Context) (adaptive.PowerObservation, error) {
		observation := observations[read]
		read++
		return observation, nil
	}
	firstResult, err := executor.Execute(context.Background(), firstInput)
	if err != nil {
		t.Fatalf("first Execute returned error: %v", err)
	}
	if firstResult.Phase != PhaseSuspended {
		t.Fatalf("first run phase = %q, want Suspended", firstResult.Phase)
	}
	if firstResult.Adaptive.Pointer.Deepest != 3 {
		t.Fatalf("first run reached tier %d, want 3", firstResult.Adaptive.Pointer.Deepest)
	}

	// Power drops again. The caller hands back the pointer exactly as persisted.
	second := &fakeAuditWriter{}
	secondInput := tieredInput(onBattery(120))
	secondInput.Adaptive.Pointer = firstResult.Adaptive.Pointer

	secondResult, err := newExecutor(second).Execute(context.Background(), secondInput)
	if err != nil {
		t.Fatalf("second Execute returned error: %v", err)
	}
	if secondResult.Phase != PhaseCompleted {
		t.Fatalf("second run phase = %q, want Completed", secondResult.Phase)
	}
	// It descends past where it stopped rather than starting over.
	if secondResult.Adaptive.Pointer.Deepest != 2 {
		t.Fatalf("second run reached tier %d, want it to continue to 2", secondResult.Adaptive.Pointer.Deepest)
	}
	// And the re-crossed tiers are reported as re-execution, not as new work.
	reexecuted := false
	for _, event := range secondResult.Adaptive.Events {
		if strings.Contains(event, "re-entered tier") {
			reexecuted = true
		}
	}
	if !reexecuted {
		t.Fatalf("expected a re-execution event on the second descent, got %#v", secondResult.Adaptive.Events)
	}
}

// Without the suspend behavior a recovered flow runs to completion, which is the pre-existing
// contract and must stay reachable rather than becoming implicit.
func TestRecoveryDoesNotSuspendWhenTheFlowDidNotAskForIt(t *testing.T) {
	writer := &fakeAuditWriter{}
	input := tieredInput(onMains())

	result, err := newExecutor(writer).Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Phase != PhaseCompleted || result.Groups != 3 {
		t.Fatalf("result = %#v, want every wave to have run", result)
	}
}

// OD-17: the pointer and the timing mode must survive an executor restart, or a restarted
// instance resumes at the wrong depth or silently reverts to a fresh mode.
func TestResumeStateCarriesThePointerAndTimingMode(t *testing.T) {
	writer := &fakeAuditWriter{}

	if _, err := newExecutor(writer).Execute(context.Background(), tieredInput(onBattery(120))); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if len(writer.resumeStates) == 0 {
		t.Fatal("expected resume state to be written")
	}
	for _, state := range writer.resumeStates {
		for _, key := range []string{"tier", "deepestTier", "pointerStarted", "timingMode"} {
			if _, present := state.State[key]; !present {
				t.Fatalf("resume state is missing %q: %#v", key, state.State)
			}
		}
	}
	last := writer.resumeStates[len(writer.resumeStates)-1]
	if last.State["timingMode"] != string(adaptive.ModeUrgent) {
		t.Fatalf("timingMode = %v, want Urgent at 120s remaining", last.State["timingMode"])
	}
}

// Unknown runtime and zero runtime are different facts. A subscriber reading the record must be
// able to tell "the device did not say" from "no time left".
func TestUnknownRuntimeIsRecordedAsAbsentRatherThanZero(t *testing.T) {
	writer := &fakeAuditWriter{}
	input := tieredInput(adaptive.PowerObservation{OnBattery: true})

	if _, err := newExecutor(writer).Execute(context.Background(), input); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	state := writer.resumeStates[0].State
	if state["runtimeSeconds"] != nil {
		t.Fatalf("runtimeSeconds = %#v, want nil for an unread runtime", state["runtimeSeconds"])
	}
}

// PL-32: a failed telemetry read is not permission to assume the good case. The flow stops
// rather than descending against a power state nobody actually observed.
func TestAFailedPowerReadStopsTheFlow(t *testing.T) {
	writer := &fakeAuditWriter{}
	executor := newExecutor(writer)
	executor.Observer = func(context.Context) (adaptive.PowerObservation, error) {
		return adaptive.PowerObservation{}, errors.New("upsd unreachable")
	}

	result, err := executor.Execute(context.Background(), tieredInput(onBattery(1200)))
	if err == nil {
		t.Fatal("expected a failed power read to surface as an error")
	}
	if result.Phase != PhaseFailed {
		t.Fatalf("phase = %q, want Failed", result.Phase)
	}
	if result.Groups != 0 {
		t.Fatalf("groups executed = %d, want none against an unread power state", result.Groups)
	}
}

// The compression is measured, not chosen: runtime available over plan remaining. Both the
// declared and effective values are recorded, along with the ratio that produced the difference.
func TestGroupTimeoutIsCompressedToFitTheMeasuredRuntime(t *testing.T) {
	writer := &fakeAuditWriter{}
	// 500s remaining, 20% reserved -> 400s available for a plan declaring 800s. Everything the
	// plan declared has to happen in half the time it asked for.
	input := tieredInput(onBattery(500))
	input.Waves = []Wave{{Index: 0, ShutdownTier: tierOf(4), Duration: 800 * time.Second, Groups: []string{"applications"}}}
	input.Groups = []Group{{Name: "applications", Action: "ScaleWorkload", Timeout: 800 * time.Second}}

	if _, err := newExecutor(writer).Execute(context.Background(), input); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	details := writer.groups[0].Details
	if details["declaredTimeout"] != (800 * time.Second).Seconds() {
		t.Fatalf("declaredTimeout = %v, want the authored 800s", details["declaredTimeout"])
	}
	if details["effectiveTimeout"] != (400 * time.Second).Seconds() {
		t.Fatalf("effectiveTimeout = %v, want 400s -- the runtime available after the reserve",
			details["effectiveTimeout"])
	}
	if details["planFits"] != true {
		t.Fatalf("planFits = %v, want true at 0.5 compression", details["planFits"])
	}
}

// The same plan under a different battery gets different compression. This is the whole reason
// the ratio is measured rather than picked: one multiplier cannot express both cases.
func TestTheSamePlanCompressesDifferentlyAgainstDifferentRuntime(t *testing.T) {
	effectiveFor := func(runtimeSeconds int64) any {
		writer := &fakeAuditWriter{}
		input := tieredInput(onBattery(runtimeSeconds))
		input.Waves = []Wave{{Index: 0, ShutdownTier: tierOf(4), Duration: 600 * time.Second, Groups: []string{"applications"}}}
		input.Groups = []Group{{Name: "applications", Action: "ScaleWorkload", Timeout: 600 * time.Second}}
		if _, err := newExecutor(writer).Execute(context.Background(), input); err != nil {
			t.Fatalf("Execute returned error: %v", err)
		}
		return writer.groups[0].Details["effectiveTimeout"]
	}

	roomy := effectiveFor(3000)
	tight := effectiveFor(400)
	if roomy != (600 * time.Second).Seconds() {
		t.Fatalf("with plenty of runtime the declared value stands, got %v", roomy)
	}
	if tight != (320 * time.Second).Seconds() {
		t.Fatalf("effectiveTimeout = %v, want 320s (400s runtime less the 20%% reserve)", tight)
	}
}

// Plentiful runtime is not a reason to overrule the flow author upward. Compression clamps at 1.
func TestCompressionNeverGrantsMoreThanTheDeclaredDuration(t *testing.T) {
	writer := &fakeAuditWriter{}
	input := tieredInput(onBattery(7200))
	input.Waves = []Wave{{Index: 0, ShutdownTier: tierOf(4), Duration: time.Minute, Groups: []string{"applications"}}}
	input.Groups = []Group{{Name: "applications", Action: "ScaleWorkload", Timeout: time.Minute}}

	if _, err := newExecutor(writer).Execute(context.Background(), input); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if got := writer.groups[0].Details["effectiveTimeout"]; got != time.Minute.Seconds() {
		t.Fatalf("effectiveTimeout = %v, want the declared 60s unchanged", got)
	}
}

// A plan that cannot be compressed enough still runs -- refusing mid-outage is worse (PL-31) --
// but the verdict is published rather than buried.
func TestAPlanThatCannotFitIsReportedAndStillRuns(t *testing.T) {
	writer := &fakeAuditWriter{}
	// 60s remaining against an hour of declared plan.
	input := tieredInput(onBattery(60))
	input.Waves = []Wave{{Index: 0, ShutdownTier: tierOf(4), Duration: time.Hour, Groups: []string{"applications"}}}
	input.Groups = []Group{{Name: "applications", Action: "ScaleWorkload", Timeout: time.Hour}}

	result, err := newExecutor(writer).Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if result.Phase != PhaseCompleted {
		t.Fatalf("phase = %q; an infeasible plan must still run", result.Phase)
	}
	if writer.groups[0].Details["planFits"] != false {
		t.Fatalf("planFits = %v, want false", writer.groups[0].Details["planFits"])
	}
}

// EX-11: timeout expiry is a group failure that engages abort policy, not an implicit success.
// A runner that returns success against an expired deadline did not finish in the allowed time.
func TestATimedOutGroupFailsTheFlow(t *testing.T) {
	writer := &fakeAuditWriter{}
	input := tieredInput(onBattery(1200))
	input.Mode = ModeEnforce
	input.Approved = true
	input.Waves = input.Waves[:1]
	// Short enough to expire inside the test. 1200s remaining puts the flow in Relaxed, where the
	// declared value passes through unscaled, so the deadline under test is exactly this one.
	input.Groups = []Group{{Name: "applications", Action: "ScaleWorkload", Timeout: 50 * time.Millisecond}}

	executor := newExecutor(writer)
	executor.Runner = runnerFunc(func(ctx context.Context, _ Action) (ActionOutcome, error) {
		// Consume the deadline the executor imposed, and report success anyway.
		<-ctx.Done()
		return ActionOutcome{Outcome: OutcomeSucceeded}, nil
	})

	result, err := executor.Execute(context.Background(), input)
	if err == nil {
		t.Fatal("expected an expired group timeout to fail the flow")
	}
	if result.Phase != PhaseAborted {
		t.Fatalf("phase = %q, want Aborted", result.Phase)
	}
	if writer.actionAttempts[0].Outcome != OutcomeTimedOut {
		t.Fatalf("outcome = %q, want TimedOut", writer.actionAttempts[0].Outcome)
	}
}

// A group with no declared timeout has no limit. Scaling "unlimited" must not invent a deadline.
func TestAGroupWithoutADeclaredTimeoutRunsUnbounded(t *testing.T) {
	writer := &fakeAuditWriter{}
	input := tieredInput(onBattery(60))
	input.Mode = ModeEnforce
	input.Approved = true
	input.Waves = input.Waves[:1]
	input.Groups = []Group{{Name: "applications", Action: "ScaleWorkload"}}

	sawDeadline := false
	executor := newExecutor(writer)
	executor.Runner = runnerFunc(func(ctx context.Context, _ Action) (ActionOutcome, error) {
		_, sawDeadline = ctx.Deadline()
		return ActionOutcome{Outcome: OutcomeSucceeded}, nil
	})

	if _, err := executor.Execute(context.Background(), input); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if sawDeadline {
		t.Fatal("an undeclared timeout must not become a deadline")
	}
}

// Wait actually waits, and it compresses on the same terms as a timeout: a pause is time the flow
// chose not to spend shutting down, so it is the first thing a short battery takes back.
func TestWaitIsCompressedLikeAnyOtherDeclaredDuration(t *testing.T) {
	writer := &fakeAuditWriter{}
	// 300s remaining, 20% reserved -> 240s available against 480s of declared plan: half.
	input := tieredInput(onBattery(300))
	input.Waves = []Wave{{Index: 0, ShutdownTier: tierOf(4), Duration: 480 * time.Second, Groups: []string{"applications"}}}
	input.Groups = []Group{{Name: "applications", Action: ActionWait, WaitDuration: 2 * time.Minute}}

	var slept []time.Duration
	executor := newExecutor(writer)
	executor.Sleep = func(_ context.Context, duration time.Duration) error {
		slept = append(slept, duration)
		return nil
	}

	if _, err := executor.Execute(context.Background(), input); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if len(slept) != 1 || slept[0] != time.Minute {
		t.Fatalf("slept = %v, want one 60s wait (half the declared 2m)", slept)
	}
}

// EX-5 makes dry-run a faithful rehearsal of everything except effects. A rehearsal that skips
// the waits reports a flow duration the real run will not reproduce.
func TestWaitIsHonoredInDryRun(t *testing.T) {
	writer := &fakeAuditWriter{}
	input := tieredInput(onMains())
	input.Waves = input.Waves[:1]
	input.Groups = []Group{{Name: "applications", Action: ActionWait, WaitDuration: time.Minute}}

	waited := false
	executor := newExecutor(writer)
	executor.Sleep = func(_ context.Context, _ time.Duration) error {
		waited = true
		return nil
	}

	result, err := executor.Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.DryRun {
		t.Fatal("expected a dry-run flow")
	}
	if !waited {
		t.Fatal("dry-run must still honor Wait, or the rehearsal reports a duration the real run will not")
	}
}

// A cancelled flow must stop waiting rather than holding the executor for a duration nobody is
// going to use.
func TestAnInterruptedWaitFailsTheGroup(t *testing.T) {
	writer := &fakeAuditWriter{}
	input := tieredInput(onBattery(1200))
	input.Waves = input.Waves[:1]
	input.Groups = []Group{{Name: "applications", Action: ActionWait, WaitDuration: time.Hour}}

	executor := newExecutor(writer)
	executor.Sleep = func(context.Context, time.Duration) error {
		return context.Canceled
	}

	result, err := executor.Execute(context.Background(), input)
	if err == nil {
		t.Fatal("expected an interrupted wait to fail the group")
	}
	if result.Phase != PhaseAborted {
		t.Fatalf("phase = %q, want Aborted", result.Phase)
	}
}

// The default sleeper must honor cancellation, since a real flow's context ends when the manager
// is shutting down -- which is exactly when a long Wait is most likely to be pending.
func TestTheDefaultSleeperReturnsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := (Executor{}).sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatalf("sleep returned %v, want context.Canceled", err)
	}
}

// An untiered flow is legitimate -- tiers are optional -- and must still report a position
// rather than silently leaving the pointer unstarted.
func TestAnUntieredFlowStillReportsAPointerPosition(t *testing.T) {
	writer := &fakeAuditWriter{}
	input := tieredInput(onBattery(1200))
	for i := range input.Waves {
		input.Waves[i].ShutdownTier = nil
	}

	result, err := newExecutor(writer).Execute(context.Background(), input)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if !result.Adaptive.Pointer.Started {
		t.Fatal("an untiered flow that executed waves must still report the pointer as started")
	}
}

// AE-2: re-descending over tiers already executed is expected, because every shutdown action is
// idempotent. It is reported so a subscriber watching a second dip can tell it from a first
// descent without diffing history itself.
func TestRedescentIsReportedAsReexecution(t *testing.T) {
	// A pointer that already reached tier 2, resumed at tier 4 after an ascent.
	state := adaptive.PointerState{Tier: 4, Deepest: 2, Started: true}
	input := AdaptiveInput{Pointer: state, FinalTier: 1, Observation: onBattery(120)}

	next, movement := descendToWaveTier(input.Pointer, Wave{ShutdownTier: tierOf(3)}, 1)

	if next.Tier != 3 {
		t.Fatalf("tier = %d, want 3", next.Tier)
	}
	if !movement.Reexecution {
		t.Fatal("descending back over an already-executed tier must be reported as re-execution")
	}
	if next.Deepest != 2 {
		t.Fatalf("deepest = %d, want it to remember 2", next.Deepest)
	}
}

// AE-4 event lines state the transition and the power observed, and never characterize the
// sequence as a dip, a flicker, or a recovery.
func TestEventLinesAreEmittedForEachWave(t *testing.T) {
	writer := &fakeAuditWriter{}

	result, err := newExecutor(writer).Execute(context.Background(), tieredInput(onBattery(1200)))
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if len(result.Adaptive.Events) < len(tieredInput(onBattery(1200)).Waves) {
		t.Fatalf("expected at least one event per wave, got %#v", result.Adaptive.Events)
	}
}

type runnerFunc func(ctx context.Context, action Action) (ActionOutcome, error)

func (f runnerFunc) RunAction(ctx context.Context, action Action) (ActionOutcome, error) {
	return f(ctx, action)
}
