# Kind Qualification: 2026-09-17

Scope: TEST-1/TEST-2/TEST-3 and the ENG-1/NS-6 acceptance gates. Task status remains in
the active and completed trackers; this document records evidence and failure analysis.

## Environment And Boundaries

- ARM64 Linux Docker host, three disposable Kind nodes, policy-enforcing Calico.
- The existing inotify preflight passed at 512 instances; its threshold was unchanged.
- Images were built from the local working tree, including the pre-existing staged planner
  refactor. This is local qualification, not proof of a published image digest or clean commit.
- A separate pre-existing Kind cluster was outside the test's ownership and preserved.
- Kind proves logical execution and simulated actuation, not physical or VM guest shutdown.

## Cancellation Qualification

`make test-kind-lifecycle` passed both partial-startup and post-API SIGTERM cases. Each
case observed three owned node IDs and verified removal of captured and cluster-labeled
containers, the runner's expected failure exit, private-state removal, preservation of
pre-existing containers, and byte-identical external kubeconfigs. The ordinary acceptance
cluster remained running throughout. The 24 runner and 23 lifecycle component tests passed.

## First Full Run

Command: `make test-e2e-nut-startup`, based on `5beef83` plus the existing staged planner work.

Passed scenarios included BYO-certificate installation and rotation, manager replacement,
metrics, admission, signal handoff, real Online/OnBattery/LowBattery transitions, SNMP decoding,
multi-node signal targeting, and driver recovery. Driver replacement took 9.099 seconds from
before fault injection, changing PID 43 to 151 while retaining the pod and container identities.

Result: 21 passed, one failed, one skipped; Ginkgo ran for 1044.989 seconds.
TEST-2 failed at the OnBattery transition after its 180-second deadline. NS-6 was skipped
because the earlier spec in the ordered Manager group failed. This run is not full-suite,
TEST-2, or NS-6 acceptance evidence. The quickstart and NetworkPolicy enforcement cases also
passed. Owned node containers and private runner state were removed, the tracked manager
kustomization was restored, and the pre-existing Kind cluster remained intact.

### Findings

- **Medium, fixture correctness:** TEST-2 assumed replacing its simulation ConfigMap would
  restart the operand. The supervisor intentionally preserves an unchanged driver stanza.
  Upstream dummy-ups retains its current timer before reopening a looped sequence; the
  initial Online fixture used a 900-second timer. Use a short repeating Online sequence so
  the real upstream driver can adopt the changed projected file within the test budget.
  This does not promise immediate reset of arbitrary running simulation timers.
  See the pinned [upstream dummy-ups parser](https://github.com/networkupstools/nut/blob/v2.8.5/drivers/dummy-ups.c#L755-L916).
- **Medium, cleanup reliability:** Manager `AfterAll` ran before the failed spec's
  `DeferCleanup`, removing the manager and CRDs before fixture cleanup. Register shared
  teardown at the BeforeAll cleanup scope so scenario cleanup runs first, including on failure.
- **Medium, test credential exposure:** the metrics fixture embedded a temporary manager
  service-account token in logged kubectl arguments; verbose curl also exposed its request
  header. Keep the token in a pod-bound projected volume and pass the header through stdin.
  Require HTTP 200 and an actual metric without logging request headers or raw response bodies.
  Raw first-run diagnostics are private evidence and must not be published.

## Fix Verification

- Focused race-enabled metrics, lifecycle, logical-flow, and registration tests passed.
- The lifecycle regression compiles the actual registration helper into a separate Ginkgo
  suite and asserts successful, failed-spec, and partial-setup cleanup order. A changed-working-
  directory regression covers the full suite's existing project-root directory change.
- Metrics regressions exercise the shell with a fake curl, verifying header input on stdin,
  credential-free arguments/output, HTTP failures, redirects, missing/invalid metrics, and timeouts.
  The fixed pod projects only its bound token and retains restricted container security.
- Sequence regressions preserve the ConfigMap identity, short Online loop, sustained OnBattery
  interval, and EOF. They do not substitute for live upstream file adoption.
- Scenario registration fingerprints remain unchanged. Repository-wide and tagged E2E lint
  report zero issues. Independent review found no security or logic blockers.
- Added-line credential/key/unsafe-shell scanning found only the explicitly synthetic
  `metrics-test-secret` used by the leakage regression; no real credential was added.

## Second Full Run

Result: 21 passed, one failed, one skipped; Ginkgo ran for 1070.097 seconds.
The hardened metrics pod passed live and emitted only HTTP 200 plus a numeric metric; the
run log contained no JWT-shaped token. Driver replacement took 13.389 seconds with pod and
container continuity. The real failure path restored fixture placement before shared manager
teardown, qualifying the cleanup-order fix.

**Medium, fixture placement:** TEST-2 stopped before creating outage workloads because Kind's
`local-path-storage` provisioner happened to occupy the selected drain worker. The relocation
list covered CNI/DNS and cert-manager, but omitted this shared deployment. The existing guard
correctly blocked drain. Include the provisioner in the same UID-guarded relocation and selector
restoration path; keep unexpected and terminating shared pods as blockers. Component regressions
cover the explicit namespace list and prohibit a blanket namespace exception.

This failure again skipped NS-6. Focused iteration retains the same private cluster, preflight,
CNI, image setup, deadlines, and ownership-checked cleanup. An explicit focus filter is not a
full-suite result; default CI remains unfiltered.

## First Focused Run

Scope: manager readiness, TEST-2, and opt-in NS-6. Result: one passed, one failed,
21 skipped; Ginkgo ran for 556.447 seconds. The short Online sequence adopted the
real OnBattery update. DryRun preserved workloads and withheld signals; Enforce
scaled, drained, delivered a signal accepted by the Simulate actuator, and recorded
the expected audit actions. The untouched pod retained its identity and remained running.

**Medium, fixture snapshot correctness:** the subsequent survivor-node assertion reused
the target's decoded Node object. Kubernetes omits false `unschedulable` values, and
decoding into an existing object retained the target's earlier true value. Decode each
successful read into a fresh typed value before replacing the caller's snapshot.
Regressions cover omitted booleans, maps, slices, repeated reads, and preserving the
prior snapshot on command or decoding failures. This is not evidence that the operator
cordoned the survivor.

Scenario cleanup also returned an error; the ordinary Ginkgo report suppressed its
details behind the initial assertion failure. Its cause remains under investigation.
The owned cluster teardown completed and the manager manifest was restored. The expired
signal control was not reached, and NS-6 was again skipped. TEST-2 remains open.

An isolated manager-readiness plus NS-6 run separates startup qualification from TEST-2's
ordered-spec failure. This is local focused iteration, not a change to the shared CI suite.

## Isolated Startup Qualification

The enabled manager-readiness and NS-6 specs both passed. The real rendered startup
observation collected 86 samples over 663.603 seconds from first driver identity to
last sample, with eight authenticated monitors and an intentional monitor replacement
that authenticated after deletion of the original. There were zero probe errors,
one unchanged driver identity, no spontaneous driver exits/replacements, and no
manager or operand pod/container restarts. The restricted security and shared-PID
sidecar assertions passed. NS-6 resource cleanup returned no errors; the manager
manifest was restored and the owned cluster containers were removed.

Observed container-runtime image identities:

- Manager: `sha256:d8baf98a62d49bbf3cfc12c48054f3298bb44b8930d392698c056b93710bc7da`.
- NUT server and supervisor: `sha256:a1cec32573083443ccf1a28cd9b1ea785e809fc8bdfda64ea9e64c8d1241e256`.
- Monitor: `sha256:730627623b1208c3c4a44330de7fd59789c9fa072a67beb3a153d9067f686077`.

**Scope:** Ginkgo reported two passed, zero failed, and 21 filtered specs in 867.729
seconds. The overall Go command returned failure because two cleanup-diagnostic
component tests failed afterward. Their failure output matches the observed test-first
RED state and lacks the new operation wrappers required by those tests. Capturing that
intermediate edit state during compilation is the likely explanation, not a confirmed
compiler-timing fact; no Ginkgo writer defect has been reproduced.
The coherent committed cleanup implementation passes its focused race tests and
tagged lint. This is successful startup-spec and cleanup evidence, not a green
overall command or image-promotion gate. The separate TEST-2 rerun uses the frozen
committed test sources. No reconstruction of the old watchdog or historical root
cause is claimed.
