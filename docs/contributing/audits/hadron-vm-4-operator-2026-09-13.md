# Hadron VM-4: the real operator on a real guest

Status: all three milestones closed, 2026-09-16. See `docs/tasks.md`'s `VM-4` entry for current
status.

## Scope, and what this milestone deliberately does not attempt

`VM-4` (`docs/tasks.md`) is to drive a simulated UPS outage through actual NUT telemetry, trigger
evaluation, planning, execution, draining, signal delivery, and guest power-off -- asserting
survivor availability, current authorization/release evidence (`F-126`/`F-127`), enforced network
policy, and audit results. Its own text is explicit that manual signal injection alone is not this
test: `VM-3` already proved the actuator's own signal-handling and halt mechanism in isolation, and
this is the flow that has to produce a real signal on its own, not one this test hand-writes.

Checked before building anything: `test/e2e` (the Kind suite) does not already cover this end to
end. It drives real `Online`/`OnBattery`/`LowBattery` transitions from a scripted `dummy-ups`
fixture (`test/e2e/e2e_test.go`, `"drives real ... transitions from a scripted dummy-ups fixture"`)
and separately hand-writes a shutdown signal directly into a `NodePowerAgent`'s projected Secret to
prove the actuator sidecar accepts it (`"delivers a projected Secret signal ..."`) -- but nothing
in `test/e2e` creates a `ShutdownFlow`, waits for trigger evaluation, and asserts execution/drain/
poweroff. `VM-4` is genuinely new ground, not a Hadron variant of an existing Kind test. Real drain
logic already exists (`internal/kubeactions/runner.go`'s `cordonNodes`/`drainNodes`/
`evictPodsOnNode`, using the real `policyv1.Eviction` subresource), but no test anywhere -- Kind or
Hadron -- exercises it against a real workload Pod yet.

This first milestone (`TestHadronOperatorManagerDeploys`, `test/hadron/operator_smoke_test.go`) is
scoped narrower than `VM-4` itself on purpose, mirroring how `VM-3` started: get the real
nut-operator manager -- CRDs, RBAC, and the controller-manager Deployment -- running inside a
Hadron guest's k3s at all, before wiring any `NUTServer`/`UPSDevice`/`NodePowerAgent`/
`ShutdownFlow` CRs or attempting any part of the actual outage flow. No prior Hadron test has run
the operator itself: `VM-2` and `VM-3` both ran bare, test-authored Pods, never the real rendered
manifests. Kind's own e2e suite does exercise these manifests already, against a real kubelet and
containerd; what a real guest adds is the same boundary `VM-3`'s own audit doc argues for the
actuator -- a kernel that is not shared with this project's own build/test-runner.

## Deployment path chosen: `config/byo-cert`, not cert-manager

`config/default` (the plain `make deploy` target) has both `../webhook` and `../certmanager` as
live, uncommented `kustomization.yaml` resources -- webhooks and cert-manager are both part of the
real production deploy, not an optional scaffold leftover. Installing cert-manager into a
throwaway single-guest Hadron test just to get a serving certificate would be a second, unrelated
thing to prove reliable. This repository already ships an alternative for exactly this reason:
`config/byo-cert`'s own comment states it plainly -- "this operator's job is to run correctly while
the cluster is losing power ... any component that has to reconcile, issue, or inject something
before admission works again is a component that can be unavailable at exactly the wrong moment."
`hack/webhook-cert.sh` mints a CA and serving certificate directly into the `webhook-server-cert`
Secret with no cert-manager dependency, and `make deploy-byo-cert` runs it as the last step of its
own recipe. This is not a test-only shortcut; it is a real, maintained production deployment path
this test simply exercises instead of the cert-manager one.

## What this milestone builds

- `test/hadron/actuator_smoke_test.go`'s inline `git rev-parse --show-toplevel` call was extracted
  into a shared `repoRootDir(t)` helper, now used by both the actuator image build and the new
  manager image build -- a test invoked as `go test ./test/hadron` runs with its working directory
  set to the package directory, not the repo root, regardless of where `go test` itself was run
  from.
- `buildManagerImageTarball` builds the real manager image via this repository's own `make
  docker-build` target (not a hand-rolled `docker build` invocation), tagged fully qualified
  (`docker.io/library/...`) from the start rather than relying on Docker's own implicit
  normalization of an unqualified tag to line up later at import/deploy time -- the same reference
  is used to build, save, import into the guest's containerd, and render into the Deployment's
  image field, so nothing has to agree on a normalization rule it never sees spelled out.
- `TestHadronOperatorManagerDeploys` boots one guest (the same `KairosAutoInstallCloudConfig` path
  every prior Hadron test has used), imports the built manager image via `guestCommandStdin` and
  `k3s ctr -n k8s.io images import -`, waits for the same default-ServiceAccount startup race
  `VM-3` found and fixed, then runs `make install` and `make deploy-byo-cert IMG=<built-image>`
  against the guest's own fetched kubeconfig (`KUBECONFIG` set in the subprocess environment, not
  reimplemented as Go API calls -- the real command path is the thing under test), and waits for
  the real `nut-operator-controller-manager` Deployment in `nut-operator-system` to report
  `ReadyReplicas >= 1`.
- `make deploy-byo-cert` mutates `config/manager/kustomization.yaml` in place (`kustomize edit set
  image`) -- the same side effect `test/e2e`'s own suite records and restores, since nothing else
  undoes it. `preserveFile` snapshots and restores it here for the same reason, so this test's run
  leaves no visible change in a working tree it does not own exclusively.
- `.github/workflows/hadron-operator-smoke.yml`: `workflow_dispatch`-only, following the same
  per-step-timeout/cleanup-before-upload/state-removal-gated-on-cleanup pattern as the other three
  Hadron smoke workflows, checked by the same table-driven `TestSmokeWorkflowsReserveCleanupBudget`.
- `buildActuatorImageTarball`'s raw `docker build` logic was generalized into
  `buildOperandImageTarball(ctx, t, repoRoot, dockerfileRelPath, namePrefix)`, now shared by the
  actuator, `nut-server`, and `upsmon-agent` image builds -- three real call sites, not a
  speculative abstraction.
- `TestHadronOperatorRunsRealUPSStack` (VM-4's second milestone) builds on the first: after the
  same manager deploy, it also builds and imports `nut-server` and `upsmon-agent` (reusing
  `buildOperandImageTarball`) alongside the already-proven `node-actuator`, then applies a real
  `UPSDevice`/`NUTServer`/`NodePowerAgent` fixture -- the exact shape `test/e2e/e2e_test.go`'s own
  `"delivers a projected Secret signal to the NodePowerAgent actuator..."` spec already proves
  against Kind, with only image references substituted for this run's locally-built ones. Checked,
  not assumed: `internal/controller/nodepoweragent_render.go`'s `renderImageReference` does not
  default an empty tag to `latest`, it omits the `:tag` suffix entirely -- so `repository` and
  `tag` are both set explicitly from the exact reference each image was actually imported under,
  rather than relying on an implicit `:latest` that would not have matched. `NodePowerAgent.spec.mode`
  is `DryRun` and `actuatorPolicy` is `Simulate`, matching that same e2e spec, since nothing in
  this milestone should be able to halt the guest. `.github/workflows/hadron-ups-stack-smoke.yml`
  follows the same pattern as the other three Hadron smoke workflows.

## Evidence

| Run | Result | Finding |
| --- | --- | --- |
| [34777697857](https://github.com/MichaelZalud18/nut-operator/actions/runs/34777697857) | **pass** (262.16s, first attempt) | The real CRDs, RBAC, and controller-manager Deployment all deployed correctly on the first try: `make install` (~18s), `make deploy-byo-cert` including `hack/webhook-cert.sh`'s CA/serving-certificate generation and `caBundle` patching (~8.5s), then the real `nut-operator-controller-manager` Deployment reached `ReadyReplicas >= 1` within ~15s more -- a real image import, real webhook admission wiring, and a real controller-manager binary starting cleanly on a real guest kernel, none of it exercised in any Hadron test before this. |
| [34778921481](https://github.com/MichaelZalud18/nut-operator/actions/runs/34778921481) | fail (`fixture apply`, 518.53s) | Guest boot, k3s readiness, the default-ServiceAccount wait, and the real controller-manager reaching Ready all succeeded again, cleanly. The fixture apply itself failed after its full 2-minute budget with only `kubectl apply: context deadline exceeded` -- a diagnostic gap in this test, not necessarily evidence about the fixture itself: `pollGuest` keeps only its check function's most recent return, and that last `kubectl apply` attempt shared the overall polling context, so it was cancelled (not genuinely failed) the instant the deadline landed mid-attempt, discarding roughly twenty-three earlier attempts' real, informative errors in favor of one timeout artifact. The true cause is still unknown. |

Fixed the diagnostic gap, not (yet) a root cause: each apply attempt now gets its own short-lived
context and logs its own error immediately, and a diagnose callback dumps a full pod listing and
the manager's own log tail. Every kind in this fixture has a mutating webhook (`test/e2e`'s own
comment on this exact fixture shape), so the manager's own webhook-server readiness is the most
likely place the next run's real error points to.

| [34780155358](https://github.com/MichaelZalud18/nut-operator/actions/runs/34780155358) | fail (`fixture apply`), diagnostics worked as designed | The new per-attempt logging immediately found the real cause instead of a timeout artifact: `Error from server: error when creating "STDIN": admission webhook "mnutserver-v1alpha1.kb.io" denied the request: json: cannot unmarshal number into Go struct field ImageReference.spec.image.tag of type string`. This test's image tags are pure-digit Unix-nanosecond timestamps, and an unquoted numeric-looking YAML scalar is parsed as a JSON number, not a string, even though `ImageReference.Tag` is a Go string field. Not a fixture-schema or webhook problem -- the admission webhooks did exactly their job, rejecting a malformed request. |

Fixed: quoted all three tag fields in the manifest template.

| [34781621589](https://github.com/MichaelZalud18/nut-operator/actions/runs/34781621589) | **pass** (547.22s) | The real `UPSDevice`/`NUTServer`/`NodePowerAgent` fixture applied cleanly, the `NodePowerAgent` reported `Ready`, and the real, operator-rendered DaemonSet pod (`hadron-agent-node-power-agent-58mz5`) was confirmed running on a real guest kernel -- the first time any Hadron test has run the real rendered manifest rather than a bare test-authored Pod, closing VM-4's second milestone. |

## Milestone 3: a real ShutdownFlow-triggered signal

`TestHadronShutdownFlowProducesRealSignal` (`test/hadron/outage_flow_smoke_test.go`) is the piece
`VM-4`'s own text calls out as the thing manual signal injection cannot stand in for: a real
`ShutdownFlow` (`mode: Enforce`) whose trigger evaluates real UPS telemetry, whose executor compiles
and runs a real wave, and whose `AgentShutdown` step writes a real signal that the already-proven
node-actuator (`VM-3`) observes and accepts. `NodePowerAgent` stays `Simulate`, so nothing here
halts the guest.

This milestone turned out to require far more real infrastructure than either prior milestone: a
real PostgreSQL audit store (SB-11 makes it a required production component for Enforce-mode
execution, not optional scaffolding), and a genuinely clear node for `AgentShutdown`'s own
node-clearance precondition (EX-9) -- neither of which the design rationale below the fold on this
doc's earlier milestones anticipated. Each was found live, from real evidence, not guessed at.

### Evidence for milestone 3

Nine failing runs, each fixing one distinct, verified real cause -- not the same failure recurring:

| # | Finding | Fix |
| --- | --- | --- |
| 1 | Enforce mode requires `safety.requireManualApproval: true` + a matching approval annotation; fixture set `requireManualApproval: false` | Set it `true`, added the approval annotation |
| 2 | The `dummy-ups` device declares no model, so it matched no capability profile and Enforce mode blocked on `DeviceUnidentified` | Set `spec.safety.allowUnidentifiedDevices: true` -- the field's own documented escape hatch for exactly this |
| 3 | The fixture's 40s `OB` window (copied from `test/e2e`, which only watches `UPSDevice.status.phase` directly) raced this test's own additional watch-latency + 1s eligibility hold before execution could start | Widened `OB` dwell to 600s |
| 4 | `ExecutionReady` stuck on `AuditStoreUnavailable`: Enforce execution requires a ready PostgreSQL audit store (SB-11), and none existed | Stood up a real `postgres:16-alpine` server (pinned by digest) plus a `PowerManagementCluster` (`storage.mode: ExternalPostgres`) referencing it |
| 5 | `PowerManagementCluster.Reconcile` never requeues on a failed audit-store connection for `ExternalPostgres` mode (unlike `CNPG`'s 5-minute requeue); applying it in the same batch as Postgres meant its one reconcile landed before Postgres was listening, and the resulting `AuditStoreNotReady` status never re-evaluated | Apply the `PowerManagementCluster` only after Postgres already reports Ready |
| 6 | A one-shot pod List right after `NodePowerAgent` reported Ready caught a DaemonSet rollout mid-transition (one Running + one Terminating pod) | Wait for exactly one Running, non-terminating pod |
| 7 | `AgentShutdown`'s node-clearance check (EX-9) blocked on `NodeNotCleared`: k3s's own stock Deployments (`local-path-provisioner`, `metrics-server`, `traefik`) are real, non-exempt, non-DaemonSet pods | Removed the ones this fixture doesn't use |
| 8 | `NodeNotCleared` persisted: CoreDNS, kept alive for this test's own Postgres DNS lookup, is itself exactly such a pod | Read the Postgres Service's ClusterIP back via its own `kubectl` call and built the DSN from that instead -- matching `nutserver_render.go`'s own F-71 precedent that the operator's components never needed cluster DNS in the first place; found and fixed a real product bug in the same pass: `upsdevice_controller.go`'s `firstTelemetryEndpoint` had never had F-71's ClusterIP-preference applied to it, so removing CoreDNS broke the manager's own telemetry polling until fixed |
| 9 | `NodeNotCleared` persisted a third time: `traefik` came back, reinstalled by k3s's own `helm-controller` from a `HelmChart` custom resource the Deployment-delete never touched | Delete the `traefik`/`traefik-crd` `HelmChart` resources themselves |

[35053758898](https://github.com/MichaelZalud18/nut-operator/actions/runs/35053758898) --
**pass** (559.20s). Real telemetry polling (with the ClusterIP fix) observed `OnBattery`; the real
`ShutdownFlow` trigger evaluator matched it, the executor compiled and ran a real wave including a
real `AgentShutdown` step against a genuinely cleared node, and the real node-actuator (`VM-3`)
logged accepting the resulting operator-written signal -- closing `VM-4`.

## Open, deliberately not attempted here

- The two-guest topology `VM-4`'s own text implies ("assert survivor availability" needs at least
  one node that stays up while another is powered off) -- every milestone so far is single-guest,
  since proving deployment, steady-state reconciliation, and signal delivery did not need a second
  node yet. `VM-2`'s `ClusterLink` is the established mechanism once a second guest is needed.
- Real drain/eviction against a live workload Pod (`internal/kubeactions/runner.go`'s
  `cordonNodes`/`drainNodes`/`evictPodsOnNode`) -- untested anywhere, Kind or Hadron. Milestone 3's
  node-clearance fix removed components this fixture never used; it did not exercise draining a
  workload that has somewhere else to go.
- Enforced network policy and audit-record assertions named in `VM-4`'s own text.
