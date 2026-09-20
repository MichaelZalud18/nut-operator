# Operator test coverage review

Components: Cross-cutting; VM Test Coverage.
Audience: contributors.

Date: 2026-09-19. This is a coverage assessment, not a new task tracker or a fresh full-suite pass.
Reviewed the working tree at HEAD `93a15f6`, including uncommitted modular work. Concurrent Hadron
changes were present and were not modified. Existing task files retain ownership of task status.

## Assessment

The test architecture is appropriate and the core safety logic has substantial coverage. The
remaining confidence gaps are concentrated in composed guest shutdown, real-cluster safety cases,
and regression wiring. Adding a large matrix of VM scenarios would be less useful than closing
those specific gaps. Test names containing "live" sometimes mean uncached reads from a fake
client; they do not by themselves establish live-cluster proof.

## Coverage by contract

| Contract | Evidence inspected | Assessment and boundary |
| --- | --- | --- |
| Inventory and deterministic planning | `internal/planner/*_test.go`, resolver/inventory tests, `test/netbox` | Cycles, ordering, domain scope, communication dependencies, identity/hash stability and real optional provider compatibility have coverage. NetBox is outside the shutdown path. |
| Telemetry and trigger decisions | NUT TLS/timeout fault tests, polling/trigger tests, executor adaptive tests, Kind simulation/SNMP specs | Protocol errors, stale/missing data, stalled transports, TLS verification and power transitions are represented. Simulated devices do not establish particular hardware behavior. |
| Approval and node targeting | Controller release validation/safety tests, executor approval/release tests, actuator tests | Revocation, generation drift, wrong pod/node, missing/future/expired telemetry, failed reads, changed destination and signal publication are explicitly asserted. Most race matrices use fake clients. |
| Quorum and concurrency | `shutdownflow_quorum_test.go`, node-claim tests, planner and terminal-handoff tests | Includes uncached-state modeling, pending signals, rechecks between writes and concurrent agents. Current Kind has one control-plane node; current VM topology also cannot prove real multi-control-plane behavior. |
| Workload actions and mixed hooks | kubeactions tests; Kind logical ShutdownFlow and modular tests | Scale/drain/order, admission, Secret delivery, HTTP/HTTPS behavior, advisory failure, dry-run and real PostgreSQL ordering are covered at appropriate layers. Kind ends at Simulate. PDB override versus unrelated API throttling is component-tested. |
| Storage failure and audit | Real PostgreSQL component suite plus spool/replay/deadline tests | Migrations, history, retention, replay, locked-writer fallback and evidence failure separated from action outcomes are represented. This is not CNPG failover qualification. |
| Managed operands and installation | Controller/envtest, image smoke, Kind driver recovery, pod restart, webhook certificate lifecycle, NUT-only acceptance | Real NUT binaries, runtime container assumptions, TLS/auth/client policy, rotations, config changes and readiness are exercised. Reapplication tests are not arbitrary version-to-version schema migration proof. |
| Garbage collection | `make test-operand-deletion`, controller envtest | A real API/GC test exists. No workflow invokes this target in the reviewed tree; ordinary envtest does not run Kubernetes garbage collection. |
| Linux/Talos host boundary | Hadron actuator and DaemonSet tests; Talos actuator tests and run evidence | Actual guest halt and invalid-signal/absent-approval controls exist. Policy-agnostic admission covers revocation; it need not be repeated as a VM case for each OS. |
| Full guest outage chain | Hadron two-node drain and network-policy tests | Implemented but current live runs fail. Drain test uses Simulate. Real drain followed by actual worker power-off with a surviving control plane is still missing as one demonstrated chain. |

The full-profile mixed-flow and clean NUT-only focused Kind passes are recorded in
[the modular acceptance audit](mod-4-5-nut-only-acceptance-2026-09-18.md). Those runs do not establish
full-suite coexistence after adding the NUT-only scenario or qualify a published final image.

## Highest-value follow-up

1. Finish VM-4's existing live milestones, then compose real telemetry, drain, actual worker halt,
   external exit evidence, survivor availability and durable audit in the same scenario. Separate
   actuator success and a Simulate drain success are useful but do not prove that composition.
2. Finish VM-2's identity/collision/cancellation acceptance. Harness failure must not masquerade
   as operator success or delete another run's resources.
3. Add EX-34's Kind integration for admitted flow-mode and agent-spec changes during an identified
   execution, and EX-35's real-API quorum publication checks in envtest. The focused review below
   explains existing coverage and why neither a duplicate approval-reader test nor three running
   control planes is needed. Physical multi-control-plane halt remains a separate qualification;
   completed F-128 implementation is not reopened.
4. Run existing fast Talos component and cleanup tests automatically under VM-5, matching Hadron's
   ordinary workflow treatment. Give the real operand-GC target a conditional automated owner. These are
   coverage-execution gaps, not reasons to create more tests with equivalent assertions.
5. Complete VM-5's exact-artifact and repeatability work before relying on guest tests as routine
   regression protection. Manual workflows already exist with deadlines, cleanup and artifacts;
   workflow existence is not a passing test or an immutable-image qualification result.

## Current VM evidence

- VM-2 join: [successful run 35298330408](https://github.com/MichaelZalud18/nut-operator/actions/runs/35298330408).
  Its remaining identity/concurrent-run checks are separately scoped in `docs/tasks.md`.
- Linux rendered DaemonSet actuator: [successful run 35357878815](https://github.com/MichaelZalud18/nut-operator/actions/runs/35357878815).
- Talos actuator: [successful full run 35281326987](https://github.com/MichaelZalud18/nut-operator/actions/runs/35281326987).
  The [dated Talos audit](talos-vm-7-actuator-2026-09-17.md) records negative cases, admission and
  guest-exit evidence separately from earlier failed attempts.
- VM-4 two-node drain: [failed run 35454896041](https://github.com/MichaelZalud18/nut-operator/actions/runs/35454896041).
- VM-4 network policy: [failed run 35454897228](https://github.com/MichaelZalud18/nut-operator/actions/runs/35454897228).
  Both are on `93a15f6`. The latter was still running during the preceding status check and has
  since completed with failure. These results establish no successful two-node acceptance.

## Focused safety follow-up

The follow-up source review narrowed the broad recommendation above. The owning tasks are
EX-34 and EX-35 under Planning & Execution Logic in `docs/tasks.md`. They follow closed
F-126/F-127 and F-128 respectively and add acceptance work for existing requirements.

**Identifier correction (2026-09-19):** the initial follow-up entries incorrectly used requirement
IDs EX-4 and EX-18 as task IDs. Those two new entries were assigned fresh IDs EX-34 and EX-35.
EX-4 and EX-18 remain unchanged requirements; the original F-126/F-127/F-128 completion records
are unchanged. This corrects the newly created follow-ups, not the historical completed tasks.

`shutdownflow_approval_test.go` already exercises the same approval-checker closure against
envtest after a real API mode change. `nodepoweragent_webhook_test.go` also establishes that
removing approval while retaining Actuate/PowerOff is rejected by admission. The fake-client
`revoked-agent` modular case can perform that write, so copying it literally into Kind would
test an impossible admitted state. EX-34 instead composes accepted flow-mode/agent-spec changes
with the running manager, immutable execution identity, cancellation, guard refresh and actual
Secret publication. A bounded hook barrier before a later wave supplies deterministic sequencing;
the assertion follows the original execution, not every later legitimately recompiled execution.
The group/wave boundary matters: flow approval is rechecked per wave, while agent identity and
authorization are rechecked before handoff. Neither contract promises cross-resource transactions.

`validateControlPlaneRelease` reads Nodes and pending signal Secrets through `r.reader()`;
EX-18 explicitly defines Ready status against declared membership, not direct etcd health.
`shutdownflow_quorum_test.go` uses fake clients for pending signals and concurrent publication.
The missing API integration can therefore be tested with real persisted Node/Secret objects in
envtest and the production runner/validator. Requiring three running control planes would add
topology and failure cost without being necessary to test this boundary. Actual HA shutdown is a
different acceptance claim and is not added as a task by this review.

Talos regression wiring belongs to existing VM-5. Its text now makes fast Talos component/cleanup
checks explicit alongside separate guest jobs; no additional Talos task was created.

## VM-8 explanation

VM-8 is maintenance of test infrastructure: extract shared guest/cluster lifecycle helpers while
keeping scenario assertions visible and OS-specific provisioning separate. It does not add an
operator feature or itself prove a guest shutdown.

There is already concrete overlap. `test/hadron/wait.go` and `test/talos/wait.go` have identical
polling implementations apart from package/build-tag/comment differences. Talos also duplicates
the checksum/download and process-exit/teardown helpers in Hadron's adapter. Hadron has dedicated
construction/teardown failure tests; Talos currently has command, polling and workflow tests but
no matching adapter/teardown unit-test files. Passing Hadron tests do not execute the Talos copy.

Useful common code would own verified artifacts, private run state, cancellation/deadlines,
process ownership and cleanup evidence, and shared negative-signal fixtures. Hadron should still
own SSH, cloud-init/k3s and image import; Talos should own machine configuration, talosctl and
registry-based image delivery. A common fixture must not require SSH. Talos's fixed forwarded
ports also mean same-host parallelism cannot be inferred from sharing the underlying PEG library.

The current task explicitly defers extraction until a third adapter. My recommendation is to
keep broad fixture redesign deferred while the live Hadron path is being debugged, but make
duplication of tested safety behavior the criterion for a later small extraction. A third OS
or a duplication-linter warning is not inherently necessary to justify sharing the existing
cleanup core. This is a recommendation, not a change to the agreed task scope.

## Documentation and review limits

The latest remote snapshot at `93a15f6` reports [Tests](https://github.com/MichaelZalud18/nut-operator/actions/runs/35454871349)
and [Lint](https://github.com/MichaelZalud18/nut-operator/actions/runs/35454871339) successful,
[Security Scan](https://github.com/MichaelZalud18/nut-operator/actions/runs/35454871345) and
[Repo Hygiene](https://github.com/MichaelZalud18/nut-operator/actions/runs/35454871297) failed, and
[Images](https://github.com/MichaelZalud18/nut-operator/actions/runs/35454871532) still running.
Failure causes for those two non-VM workflows were not diagnosed in this review; a scan failure
alone does not establish a vulnerability. This is not an all-green CI snapshot and does not
qualify uncommitted modular changes against the remote revision.

`test-domains.md` needs reconciliation with current evidence: it still lists VM-3 and Talos
actuation as absent, calls VM-5 unstarted, and implies quorum is covered by the current Kind
shape. Its static-scanner wording also overstates what scans can prove. `docs/tasks.md` retains
VM-8-before-Talos dependency wording despite completed Talos work and VM-8's explicit deferral.
These are documentation inconsistencies, not evidence that completed actuator tests are absent.

During this review, `go test -tags=talos ./test/talos -race -count=1 -timeout=2m` passed in 1.096s.
No VMs or clusters were started, no running workflows were changed, and no full suite was rerun.
This is a risk-based source/workflow/evidence review, not a line-coverage or mutation score.
Recovery orchestration, restart/resume continuity, agents-only/external execution profiles and
USB/PDU actuation outside the approved v1 scope are not treated as missing v1 tests.

## Execution-safety implementation evidence (2026-09-19)

The EX-35 follow-up adds five envtest cases using the production publication runner and complete
release validator with a direct API reader. Real persisted Node, Pod, UPSDevice, NUTServer,
NodePowerAgent and Secret fixtures establish pending-voter accounting, unavailable-peer refusal,
competing channel serialization, and terminal create/update behavior. Synthetic readiness and
telemetry satisfy the ordinary guards; an unready agent Pod blocks a complete terminal batch
without publishing partial keys. This is API integration evidence against declared membership.
The controller, kubeactions and executor suites pass with race detection.

While adding EX-34's original-execution audit assertions, a context-aware recorder reproduced a
cancellation defect: action cancellation also prevented the terminal execution row from replacing
its earlier Running state. `TestCanceledExecutionRetainsTerminalAudit` failed before the correction
and passes afterward for action, advisory-hook, final-hook and power-observation cancellation.
Only the terminal abort write receives a detached context with a one-second deadline. A stalled
writer reports its deadline failure while execution stays aborted; action contexts remain canceled.
The ordinary Go suite and executor/controller lint pass with the correction.
