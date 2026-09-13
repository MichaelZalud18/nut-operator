# Hadron VM-4: the real operator on a real guest

Status: design rationale and first milestone, 2026-09-13. See `docs/tasks.md`'s `VM-4` entry for
current status.

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

## Evidence

| Run | Result | Finding |
| --- | --- | --- |
| _(not yet run live)_ | | |

## Open, deliberately not attempted here

- Any `NUTServer`/`UPSDevice`/`NodePowerAgent`/`ShutdownFlow` CR, or anything about the actual
  outage flow -- this milestone only proves the manager itself runs.
- The two-guest topology `VM-4`'s own text implies ("assert survivor availability" needs at least
  one node that stays up while another is powered off) -- this milestone is single-guest, since
  proving the deployment mechanism at all does not need a second node yet. `VM-2`'s `ClusterLink`
  is the established mechanism once a second guest is needed.
- Real drain/eviction against a live workload Pod (`internal/kubeactions/runner.go`'s
  `cordonNodes`/`drainNodes`/`evictPodsOnNode`) -- untested anywhere, Kind or Hadron, before this.
- Enforced network policy and audit-record assertions named in `VM-4`'s own text.
