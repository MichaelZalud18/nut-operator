# Test Domains

What each layer of testing in this repository actually proves, which files and workflows own it,
and what nothing yet proves. Read this before adding a test to decide which domain it belongs in,
and before citing a passing suite as evidence for something a different domain is responsible for.

A pass in one domain is not evidence for another: a mocked `Machine` interface proves the calling
code handles a stop error correctly, not that a guest shut down; a Kind node is a container, not a
VM, and cannot prove anything that depends on a real kernel.

## Covered

### Unit and component tests

**Proves:** package-level logic in isolation — compilation, matching, protocol parsing, state
machines — against fakes and in-memory fixtures. No real Kubernetes API server, no real guest, no
real network.

**Owns:** `go test ./api/... ./cmd/... ./internal/... ./test/utils`, run on every push and pull
request across three `ENVTEST_K8S_VERSION` values (`.github/workflows/test.yml`). Every
`internal/*` package listed in `docs/tasks.md`'s component sections has coverage here; see that
file's `F-n` entries for specific open gaps within this domain.

### envtest (controller and webhook)

**Proves:** reconciliation and admission logic against a real API server and etcd — real watches,
real conflicts, real validation — but no real kubelet and no real node. The rendered actuator Pod
spec is checked as data; kubelet never evaluates it.

**Owns:** `internal/controller/*_test.go`, `internal/webhook/v1alpha1/*_test.go`. Part of the same
`make test` run as unit tests, using `sigs.k8s.io/controller-runtime/tools/setup-envtest`-provisioned
binaries.

### Kind end-to-end

**Proves:** multi-node cluster behavior — real Calico `NetworkPolicy` enforcement, real Pod
scheduling and eviction, driver-recovery timing, image compatibility — using simulated UPS devices
(`dummy-ups`, `snmpsim`) rather than real hardware. This is where quorum, drain-sequencing, and
policy-enforcement logic complexity belongs: Kind makes multi-node scenarios cheap and repeatable,
even though its nodes are containers, not VMs.

**Owns:** `test/e2e/*_test.go` (driver recovery, pod restart, multi-node, network policy, cert
lifecycle, soak runs, required-image presence), gated behind the `e2e` build tag.
`.github/workflows/test-e2e.yml` runs it on pull requests (building images from the checkout) and
via `workflow_call` from `images.yml` on push to `main` (against the images just published).
`make check-test-e2e-host` gates the host's inotify limits before a local run.

The `TEST-2` scenario connects dummy-ups telemetry to production trigger evaluation, planning,
ordered workload actions, signal publication, and the rendered Simulate actuator. It includes
approval, DryRun, stale-signal, audit-order, and untouched-workload controls. This tests logical
orchestration, not host power-off; current live qualification is tracked in `docs/tasks.md`.

`make test-kind-lifecycle` separately exercises cancellation during real cluster startup and
after API ownership is established. The observer checks owned container removal and unchanged
external kubeconfigs; it never deletes containers itself. Its manual workflow supplements the
normal full-suite/image gate. Cluster-free runner/rehearsal tests prove the checks' behavior,
not successful live teardown. Failed attempts retain private evidence, not proof of cleanup.

### Real NetBox service

**Proves:** compatibility of the shipped importer with the pinned real NetBox REST API: token
authentication, pagination, DCIM cables, custom metadata, filtering, deterministic topology and
CR output, and provider-neutral inventory compilation. Invalid provider data must fail closed.
It does not prove Kubernetes reconciliation, NUT connectivity, or shutdown behavior.

**Owns:** `test/netbox/`, `hack/test-netbox.py`, and `.github/workflows/test-netbox.yml`.
`make test-netbox` creates owned disposable NetBox/PostgreSQL/Redis resources on a private Docker
network. The conditional workflow follows importer/contract/dependency/fixture changes; normal
Kind runs and shutdown execution have no NetBox service dependency. See the
[service fixture](../../../test/netbox/README.md) for coverage and cleanup boundaries.

### Static analysis and supply chain

**Proves:** no committed secrets, no known-vulnerable dependencies, no common insecure code
patterns, and an accurate SBOM. Says nothing about runtime behavior.

**Owns:** bandit, checkov, detect-secrets, grype, npm-audit, semgrep, syft, orchestrated by AWS
Labs ASH (`make security-scan`, `.github/workflows/security.yml`). Scanner pins and exclusions live
in the `Makefile`, not duplicated here.

### Manifest and repo hygiene

**Proves:** samples validate against the generated CRD schemas, committed install bundles match
the manifests they were generated from, and no private IP literal has leaked into a public path.

**Owns:** `.github/workflows/hygiene.yml`, `hack/validate-samples.py`, `hack/validate-installers.sh`.

### Hadron VM boundary (partial)

**Proves:** behavior that depends on a real guest kernel — real `reboot(2)`, a capability that
survived the image build and registry round trip, genuine host PID namespace membership, real
kubelet Pod Security Admission — which no container-based test, including Kind, can exercise at
all. Deliberately narrow in scope (see `docs/tasks.md`'s Hadron VM Test Coverage section for why);
it does not re-prove logic Kind already covers.

**Owns:** `test/hadron/*` (build-tag-gated `hadron` for fast component tests, `hadron_smoke` for
the real-KVM boot test), `.github/workflows/hadron-vm-probe.yml` (`VM-1`, runner feasibility —
closed, see `docs/contributing/audits/hadron-vm-1-feasibility-2026-09-11.md`) and
`.github/workflows/hadron-vm-boot-smoke.yml` (`VM-2`, single-guest unattended install to a real
Ready k3s node — see `docs/contributing/audits/hadron-vm-2-peg-evaluation-2026-09-11.md` for the
live-run evidence). Both workflows are `workflow_dispatch`-only, not required or automatic checks.

## Not yet covered

- **Real guest actuator proof (`VM-3`, `docs/tasks.md`).** `hack/verify-actuation.sh`'s own header
  names exactly what only a real run can prove — kubelet's real Pod Security Admission decision,
  whether `CAP_SYS_BOOT` survived the build/registry pipeline, whether `raiseSysBoot()` actually
  moves the capability into effect, genuine host PID namespace membership, and real
  signal-delivery timing. The script exists but is **manual, not CI**: it takes `NODE`/`AGENT` env
  vars against an already-existing real cluster and node. Nothing runs it automatically anywhere.
  `VM-3` is the planned automated replacement, against a disposable Hadron guest instead.
- **Full outage-to-poweroff integration on a real guest (`VM-4`).** Trigger evaluation, planning,
  execution, draining, signal delivery, and guest power-off, end to end, against a real guest and
  real NUT telemetry. Not built. Once it exists, it is also the first real-guest cross-check for
  `F-126` (approval revocation) and `F-127` (stale-target refresh) — see those entries in
  `docs/tasks.md`.
- **Talos actuation.** `cmd/node-actuator/talos.go` has unit coverage only (fakes, no real Talos
  API). No disposable-Talos-VM or sacrificial-node integration test exists, and Hadron cannot
  supply one — Hadron boots Linux/cOS, not Talos. A separate `TalosShutdown` harness would be
  needed; not scoped anywhere yet.
- **Control-plane quorum under real conditions (`F-128`).** Fake-client-tested only today. `VM-4`
  as scoped will not close this either: quorum needs at least three control-plane-role guests to
  test against, and the harness's stated scope is one control-plane VM plus one disposable worker.
- **Physical, real-UPS hardware evidence.** `docs/tasks.md`'s Validation Gates section leaves this
  an open question — "whether a live plug-pull is also a v1 gate" — not built, not committed to
  either way. Informal, non-CI validation against real hardware is understood to happen outside
  this repository; nothing in this repo's automation depends on it or claims to cover it.
- **Concurrent multi-VM isolation at the Hadron layer.** Component-tested for a single guest in
  isolation only (unique state directory, unique sampled port); never exercised with two guests
  actually running at once in the same job. Tracked under `VM-2`'s open safety checks.
- **Hadron in CI (`VM-5`) and its public documentation (`VM-6`).** Both unstarted, both gated on
  `VM-2`/`VM-3` proving reliable first.
