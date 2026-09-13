# Kind Component Boundary Investigation

Scope: F-146. This is a dated investigation, not a CI health report or a proposed suite split.

## Decision So Far

Retain the shared Kind suite and exact-image promotion gate. The reviewed log does not support
attributing its overall runtime primarily to shared image setup. Narrow component tests are useful
alongside it; a separate Kind cluster per component could duplicate startup and teardown costs.
Comparative measurements remain open before claiming savings or restructuring CI.

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
