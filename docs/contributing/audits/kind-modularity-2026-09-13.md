# Kind Component Boundary Investigation

Scope: F-146. This is a dated investigation, not a CI health report or a proposed suite split.

**Task scope superseded 2026-09-17:** [OM-1](../../tasks.md#operator-maturity--hardening)
now owns targeted efficiency analysis. The controlled matrix below is historical, not an active
completion requirement. Apply controlled comparisons to a concrete proposed optimization.
The original F-146 entry is retained under [superseded tasks](../../tasks-completed.md#superseded-tasks).

## OM-1 Decision, September 17

**Keep the shared cluster, full suite, immutable-image promotion gate, and current deadlines.**
OM-1's scoped investigation is complete; the record is in
[completed tasks](../../tasks-completed.md#operator-maturity--hardening). No optimization or
speedup is claimed. The comparison matrix below belongs to superseded F-146, not a remaining gate.

Re-read the API job metadata and decoded timestamped log for successful job `103604795592`,
run `34712583914`, attempt 1, push revision `a1e76b76ffe4d19183a87f5e50ad6b5f48789476`.
This is a published-image run, not a source-build PR measurement. Go and local-tool caches
explicitly reported hits; that does not establish all Docker layers or filesystem caches were warm.

Ranked, non-overlapping wall-clock intervals within that trace:

| Rank | Interval | Observed cost |
| --- | --- | ---: |
| 1 | Scenario setup, execution, and cleanup, 19:05:17.251 to 19:15:07.375 | 590.124s |
| 2 | Test-command start to BeforeSuite, 19:01:32 to 19:04:30.247 | about 178.247s |
| 3 | Shared BeforeSuite | 47.004s |
| 4 | AfterSuite | 13.313s |

The second interval includes generation/build checks, cluster/CNI setup, and test compilation;
it is not all cluster startup. Cluster creation itself spans about 33.003s (19:03:22.685 to
19:03:55.688); CNI setup to Ready spans about 29.837s (19:03:56.078 to 19:04:25.915).
Those are subdivisions, not additional costs to add to the ranking. About 34s of job time
remains outside these four intervals, including workflow setup/post steps and command/log gaps.

The longest scenario step-to-next-step intervals were signal handoff (92.104s), BYO-certificate
manager readiness (65.294s), fanout convergence (64.200s), and fanout signal acceptance (40.997s).
These include real reconciliation/projection/startup waits, not proven idle time. Scripted
OnBattery and LowBattery observation added 31.654s and 40.132s respectively; those exercise real
telemetry timing and must not be deleted or silently replaced by synthetic status writes.

### Unsuccessful And Partial Observations

Retain failures and cancellations as separate observations, not successful completion samples:

- Runs `35225195618` and `35271792237` failed before E2E as recorded below.
- [Run 35276345446](https://github.com/MichaelZalud18/nut-operator/actions/runs/35276345446),
  revision `810c62c`, canceled Kind job `105390479267` after 17m47s. BeforeSuite completed in
  42.101s; the log reached the new shutdown-flow fixture setup before cancellation. Signal
  handoff took 5.216s in this attempt, while fanout signal acceptance took 71.622s. These differ
  substantially from the successful trace and caution against attributing a single slow sample
  to a fixed bottleneck. The suite, revision, and execution order differ; no A/B speedup follows.
  The canceled job supplies neither full-suite acceptance nor confirmed teardown evidence.
- [Run 35278691189](https://github.com/MichaelZalud18/nut-operator/actions/runs/35278691189),
  revision `9dd97b8`, canceled during image/TLS jobs; Kind was skipped. It has no Kind duration.
- Run `35279101851` at `6469108` was still building the manager at inspection. It is not included
  as a completed timing or qualification result. No retries were measured in this scoped sample.

### Targeted Follow-Up Policy

If optimizing later, first measure per-scenario convergence and signal-delivery latency across
comparable runs. A controlled change should address the measured delay while retaining the same
assertions, production paths, and failure bounds. Next examine the generation/compilation portion
of the 178s pre-suite interval with phase timers; do not assume cache hits make it negligible.
Shared image loading is a lower-priority target: production pull/load was only 16.188s here.

The evidence does not justify a split cluster per component, parallel mutations of shared
fixtures, shorter acceptance deadlines, or removal of network-policy/real-image coverage.
No resource measurements, PR/source-build timings, or controlled cache comparisons were captured;
those limitations bound this decision rather than creating an exhaustive new benchmark task.

## Historical Decision And Evidence

Retain the shared Kind suite and exact-image promotion gate. The reviewed log does not support
attributing its overall runtime primarily to shared image setup. Narrow component tests are useful
alongside it; a separate Kind cluster per component could duplicate startup and teardown costs.
Comparative measurements remain open before claiming savings or restructuring CI.

### September 17 Qualification Check

The decision remains **keep shared setup; comparison not qualified**. Run
[35271792237](https://github.com/MichaelZalud18/nut-operator/actions/runs/35271792237)
at `d925fda` failed module tidiness before E2E. Run
[35225195618](https://github.com/MichaelZalud18/nut-operator/actions/runs/35225195618)
at `f5d9c72` failed the cluster-free harness before cluster creation. Retain these as failed
pipeline observations, not zero-duration Kind runs or successful setup measurements.
The September 17 local `make check-test-e2e-host` also failed before creating a cluster:
128 inotify instances were available, against the existing 512-instance requirement.
No guardrail was bypassed and no host setting was changed.

F-146 cannot be closed from those observations. The next controlled experiment needs a
provisioned disposable runner, a fixed revision, and a fixed set of production image digests.
Use the following record for every attempt, including unsuccessful attempts:

- Revision, source-build/published-image path, full/focused scenario inventory, runner class,
  architecture, Kubernetes/CNI versions, and exact production/fixture image identities.
- Explicit cache preparation and observed cache hits. A new job or a faster run alone does
  not prove a cold or warm cache. Never clear a shared host's caches for an experiment.
- At least two repetitions of each focused/full x source-build/published-image x cold/warm cell.
  Pair comparisons on the same inputs; report the small sample size rather than a stable average.
- Separate generation/compilation, cluster/CNI setup, image build/pull/load, BeforeSuite,
  scenario setup/execution/cleanup, AfterSuite, and cluster deletion. Unmeasured intervals stay
  unknown; workflow-step timestamps alone cannot separate all of these.
- Peak resource observations with the sampling interval and scope. Runner process memory does
  not include all Docker container memory, and CPU time is not wall time.
- Outcome, attempt number, failure phase, cancellation point, and verified cleanup. Keep retries
  as distinct attempts; canceled durations are partial costs, not completion times.

Compare selective fixture setup with the unchanged shared suite before proposing any split.
For a split proposal, include repeated cluster/CNI/manager/image setup and maintenance cost,
not just the sum of assertion durations. Component tests complement integration coverage;
they cannot replace exact-image or network-policy acceptance. TEST-1/TEST-3 own live scenario
and cancellation qualification; this investigation consumes their evidence, not a second
independent cleanup implementation. Required-check names and promotion dependencies stay intact.

## Direct Evidence

Inspected job `103604795592` in
[run 34712583914](https://github.com/MichaelZalud18/nut-operator/actions/runs/34712583914),
a successful main image-promotion pipeline from September 12. Metadata and timestamped job logs
were retrieved September 13. This is one successful detailed trace, not an average or flake rate.

| Interval | Observed duration |
| --- | ---: |
| Whole Kind job, 19:01:05 to 19:15:28 UTC | 14m 23s |
| Test command step, 19:01:32 to 19:15:22 | 13m 50s |
| BeforeSuite, reported by Ginkgo | 47.004s |
| Four production-image pull/load operations, 19:04:30.247 to 19:04:46.435 | 16.188s |
| SNMP fixture build/load, 19:04:46.435 to 19:05:02.634 | 16.199s |
| Certificate-manager check/install to end of BeforeSuite | about 14.6s |
| Scenario interval after BeforeSuite to AfterSuite, 19:05:17.251 to 19:15:07.375 | about 9m 50s |
| AfterSuite, reported by Ginkgo | 13.313s |

BeforeSuite is about 5.4% of this job, not its dominant cost. The scenario interval includes
per-scenario deployment, waiting and cleanup; it is not all assertion CPU time. Earlier command
time includes generation, compilation and cluster/CNI setup and is not cleanly separated here.
The timestamp intervals include small logging/scheduling gaps rather than exact operation timers.

Examples within the scenario interval: a fanout agent convergence wait took about 64s; its signal
acceptance wait about 41s. A BYO-certificate manager readiness wait took about 65s. These are
observations, not proof of unnecessary waits or permission to shorten acceptance bounds.

## Dependency Map

- Manager/admission/metrics and upgrade scenarios need the manager, CRDs, and an installation path.
- Driver recovery and dummy/SNMP telemetry exercise the actual operator-to-NUT wiring; SNMP adds
  the test-only simulator image.
- Fanout and restart scenarios exercise rendered node agents, projected signals and Kubernetes
  convergence; replacing those with package tests would lose their integration evidence.
- Network-policy scenarios require a policy-enforcing CNI and actual allowed/denied traffic.
- BYO-certificate installation intentionally exercises a distinct installation and rotation path.

`BeforeSuite` prepares all five images regardless of spec filtering. On PR/local paths they are
built if no image references are supplied. Main promotion supplies four immutable published image
references, so that path pulls those artifacts; only the unpublished SNMP fixture builds locally.
The shared fixture supports reuse across the full suite and catches mismatched production artifacts.

## Remaining Measurements

Compare focused and full runs on controlled revisions, including PR/source-build and
promotion/published-image paths, warm/cold caches, resource use and duplicate setup costs. Include
failed attempts and retries separately; canceled runs are incomplete observations, not successful
completion times. Existing aggregate history must not be presented as a controlled comparison.

Use Ginkgo reports or explicit phase timings to separate generation/compilation, cluster/CNI,
pull/build/load, per-scenario setup/execution/cleanup and final teardown. Preserve all scenarios,
image identity checks and cleanup guarantees in any subsequent proposal. Current evidence supports
keeping the suite, not removing tests or promising a specific speedup.
