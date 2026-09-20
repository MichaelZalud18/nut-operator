# VM framework comprehensive source review — 2026-09-20

Components: VM Test Coverage.
Audience: contributors.

## Scope and method

The original two-module extraction was insufficient to substantiate a comprehensive framework
review. This review enumerates the original Hadron and Talos harnesses, including smoke-tagged
code, workflow definitions, emergency cleanup, and supporting build/provisioning entry points.
The [machine-readable snapshot](vm-framework-inventory-2026-09-20.json) contains 56 files and
194 Go functions, with source hashes and function locations. Reproduce it with
`go run ./test/internal/vmframework/cmd/inventory` from the repository root.

Token comparison identifies 18 nontrivial exact duplicate groups and 21 structural candidate
groups. The structural set overlaps the exact set; these are not 39 independent duplications.
Exact comparison ignores comments and formatting. Structural comparison also normalizes names
and literals and therefore requires semantic review. Tiny matches remain recorded separately.
The detector compares whole functions, not every possible repeated statement block. File hashes
identify the reviewed source; subsequent edits require regeneration and renewed review.

## Common mechanics and boundaries

Exact duplicate families include repository lookup, ISO download/checksum handling, teardown,
process lookup, argument append, node readiness, file preservation, make/kubectl execution,
image-reference splitting, polling and wait tests. Additional structural families include signal
writing, rejected/revoked signal observation and invalid payload matrices.

Repeated scenario sub-blocks also construct guests, register cleanup, await SSH/API readiness,
retrieve kubeconfig, construct clients, build/deliver images, deploy manager/certificates, and
apply fixtures. These blocks are not all exact whole-function matches. Share their mechanisms
without hiding scenario ordering or acceptance criteria:

| Concern | Shared implementation | Adapter/scenario responsibility |
| --- | --- | --- |
| Artifacts and polling | `artifact`, `readiness` | Guest configuration, diagnostic content and timing policy. |
| Host execution | `command` | Explicit environment/credentials, safe diagnostic publication, transport arguments. |
| Editable configuration | `workspace` | Select bounded source subtree and retain failure artifacts; no edit-and-restore of shared checkout. |
| Resource lifetime | `lifecycle`, existing `vmprocess` | Register ownership before startup, verify each resource and supply cooperative stop callbacks. |
| Kubernetes observations | `kube` | Acquire private config and trusted expected cluster UID; qualify stricter pod readiness on real scenarios. |
| Negative signals | `signalfixture` | Prove delivery to a healthy, continuously observed actuator and actual rejection. |
| Image references/builds | `image` | Archive import for Hadron; reachable registry/mirror for Talos; actual build execution and cleanup. |
| Workflow policy | `workflow` | Scenario-specific budgets and credentials; declarative checks do not interpret arbitrary expressions/shell. |
| Emergency cleanup | `hack/vm_cleanup.py` | Independent fallback after Go failure; wire each workflow during adoption and retain diagnostics. |

The tiny `withArgs` append helper does not warrant its own abstraction. Raw `machineProcess`
lookups can adopt `vmprocess.Exited`, which observes the retained startup handle, but confirmed
process exit still does not prove guest-initiated shutdown. Shared Python emergency cleanup uses
argv/path matching and pidfds; it does not provide Go's executable verification. It remains
independent of the Go process. Neither cleanup path constitutes actuation evidence.

Hadron cloud-init, SSH, k3s join and ClusterLink networking differ materially from Talos API
bootstrap and machine configuration. Image delivery differs too. Do not erase these boundaries
behind an interface that only forwards adapter-specific parameters. Existing Makefile build and
webhook certificate targets already have shared owners and should be reused.

## File dispositions

Every source file in the snapshot is accounted for below. Function locations and duplicate group
membership are in the linked JSON; the table states the extraction boundary, not migration status.

| Source | Disposition |
| --- | --- |
| `.github/workflows/hadron-actuator-daemonset-smoke.yml` | Covered by shared smoke-workflow budget/order validator; retain scenario invocation and guest-specific provisioning. |
| `.github/workflows/hadron-actuator-smoke.yml` | Covered by shared smoke-workflow budget/order validator; retain scenario invocation and guest-specific provisioning. |
| `.github/workflows/hadron-cluster-join-smoke.yml` | Covered by shared smoke-workflow budget/order validator; retain scenario invocation and guest-specific provisioning. |
| `.github/workflows/hadron-cluster-link-smoke.yml` | Covered by shared smoke-workflow budget/order validator; retain scenario invocation and guest-specific provisioning. |
| `.github/workflows/hadron-operator-smoke.yml` | Covered by shared smoke-workflow budget/order validator; retain scenario invocation and guest-specific provisioning. |
| `.github/workflows/hadron-outage-flow-smoke.yml` | Covered by shared smoke-workflow budget/order validator; retain scenario invocation and guest-specific provisioning. |
| `.github/workflows/hadron-outage-flow-two-node-network-policy-smoke.yml` | Covered by shared smoke-workflow budget/order validator; retain scenario invocation and guest-specific provisioning. |
| `.github/workflows/hadron-outage-flow-two-node-smoke.yml` | Covered by shared smoke-workflow budget/order validator; retain scenario invocation and guest-specific provisioning. |
| `.github/workflows/hadron-ups-stack-smoke.yml` | Covered by shared smoke-workflow budget/order validator; retain scenario invocation and guest-specific provisioning. |
| `.github/workflows/hadron-vm-boot-smoke.yml` | Covered by shared smoke-workflow budget/order validator; retain scenario invocation and guest-specific provisioning. |
| `.github/workflows/hadron-vm-probe.yml` | Keep kernel-only KVM probe lifecycle separate from full guest smoke jobs; do not impose their cleanup-step contract. |
| `.github/workflows/talos-actuator-smoke.yml` | Covered by shared smoke-workflow budget/order validator; retain scenario invocation and guest-specific provisioning. |
| `.github/workflows/talos-vm-boot-smoke.yml` | Covered by shared smoke-workflow budget/order validator; retain scenario invocation and guest-specific provisioning. |
| `Makefile` | Reuse existing manager provenance, build, deployment and certificate targets; no parallel build policy. |
| `hack/hadron-cleanup.py` | Adopt shared Python emergency cleanup with guest root selection; preserve independently executable fallback. |
| `hack/hadron-vm-probe.sh` | Keep kernel-only KVM probe lifecycle separate from full guest smoke jobs; do not impose their cleanup-step contract. |
| `hack/talos-cleanup.py` | Adopt shared Python emergency cleanup with guest root selection; preserve independently executable fallback. |
| `hack/test_hadron_cleanup.py` | Retain independent fallback regression tests until callers migrate; shared Python suite exercises both root types. |
| `hack/test_talos_cleanup.py` | Retain independent fallback regression tests until callers migrate; shared Python suite exercises both root types. |
| `hack/webhook-cert.sh` | Reuse existing shared certificate preparation; pass private kubeconfig/environment at execution boundary. |
| `test/hadron/actuator_daemonset_smoke_test.go` | Share invalid fixtures and pod identity observations; retain delivery/rejection observation windows and rendered-agent acceptance. |
| `test/hadron/actuator_smoke_test.go` | Share repository lookup, build plans, kubectl execution, workspace, invalid signal encoding and observations; retain actuation transport and shutdown-cause assertions. |
| `test/hadron/adapter.go` | Share artifact preparation and verified process ownership; keep PEG guest configuration and constructor policy in each adapter. |
| `test/hadron/adapter_test.go` | Retain adapter configuration regressions; shared artifact tests cover download/publication semantics. |
| `test/hadron/cloudinit.go` | Hadron-specific cloud-init, k3s installation and join configuration; retain in adapter. |
| `test/hadron/cloudinit_test.go` | Retain Hadron provisioning contract tests. |
| `test/hadron/cluster_join_smoke_test.go` | Keep two-guest join, distinct node identity and topology acceptance; share bounded kube observations and cleanup. |
| `test/hadron/cluster_link_smoke_test.go` | Keep Hadron bridge/link acceptance; share lifecycle cleanup only. |
| `test/hadron/command.go` | Keep guest SSH transport/quoting local; shared command module owns host subprocess execution. |
| `test/hadron/command_test.go` | Retain transport-specific quoting tests alongside shared subprocess tests. |
| `test/hadron/kubeconfig.go` | Keep SSH retrieval and endpoint rewriting local; consume explicit bytes with shared kube client. |
| `test/hadron/kubeconfig_test.go` | Retain Hadron endpoint and kubeconfig acquisition tests. |
| `test/hadron/network.go` | Keep Hadron ClusterLink topology and guest networking local; compose resource cleanup with lifecycle. |
| `test/hadron/network_test.go` | Retain bridge/address/topology regressions; these are not Talos bootstrap contracts. |
| `test/hadron/operator_smoke_test.go` | Use private workspace, command and manager image plans; retain deployment and webhook readiness assertions. |
| `test/hadron/outage_flow_smoke_test.go` | Share fixture execution mechanics; retain authorization, audit and outage-to-halt evidence. |
| `test/hadron/outage_flow_two_node_network_policy_smoke_test.go` | Retain network policy enforcement and allowed/denied flow assertions; share execution mechanics. |
| `test/hadron/outage_flow_two_node_smoke_test.go` | Share lifecycle and kube observations; retain drain ordering, survivor availability and two-node shutdown evidence. |
| `test/hadron/readiness.go` | Use shared kube readiness observations; retain Hadron-specific diagnostic collection. |
| `test/hadron/readiness_test.go` | Retain adapter readiness regression while adopting stricter shared conditions. |
| `test/hadron/smoke_test.go` | Keep real Hadron boot/SSH acceptance; compose artifact, readiness and lifecycle mechanics. |
| `test/hadron/teardown_test.go` | Keep pinned PEG integration and adapter cleanup tests; share vmprocess ownership and lifecycle coordination. |
| `test/hadron/ups_stack_smoke_test.go` | Share command/readiness/client mechanics; retain real NUT stack and telemetry assertions. |
| `test/hadron/wait.go` | Adopt shared readiness.Wait with explicit budgets and adapter diagnostics. |
| `test/hadron/wait_test.go` | Shared cancellation tests cover common loop; retain adapter wiring regression. |
| `test/hadron/workflow_test.go` | Use shared workflow validator; keep guest-specific workflow discovery and scenario expectations. |
| `test/talos/actuator_smoke_test.go` | Share repository lookup, build plans, kubectl execution, workspace, invalid signal encoding and observations; retain actuation transport and shutdown-cause assertions. |
| `test/talos/adapter.go` | Share artifact preparation and verified process ownership; keep PEG guest configuration and constructor policy in each adapter. |
| `test/talos/boot_smoke_test.go` | Keep real Talos boot/API acceptance; compose artifact, readiness and lifecycle mechanics. |
| `test/talos/registry_smoke_test.go` | Share tagged image descriptors/build plans and cleanup coordination; keep Talos registry/mirror delivery and ownership local. |
| `test/talos/talosctl.go` | Keep Talos API bootstrap, credentials and machine configuration local; share host execution and bounded readiness. |
| `test/talos/talosctl_test.go` | Retain Talos command/configuration contracts; shared command tests do not replace them. |
| `test/talos/teardown_test.go` | Keep pinned PEG integration and adapter cleanup tests; share vmprocess ownership and lifecycle coordination. |
| `test/talos/wait.go` | Adopt shared readiness.Wait with explicit budgets and adapter diagnostics. |
| `test/talos/wait_test.go` | Shared cancellation tests cover common loop; retain adapter wiring regression. |
| `test/talos/workflow_test.go` | Use shared workflow validator; keep guest-specific workflow discovery and scenario expectations. |

## Contracts and qualification

The framework tests exercise real harmless subprocesses (including child/orphan cancellation),
private directory copies, fake Kubernetes observations, the actual actuator signal parser,
checked-in workflow YAML, pinned HTTP artifacts and both real adapter constructors. Composition
contracts combine private workspace, image and kubectl plans, signal encoding and lifecycle
cleanup; they do not execute Docker/kubectl or boot guests. See the
[module contracts](../../../test/internal/vmframework/README.md) for limits and test commands.

Adoption must qualify real boot/provisioning and cleanup against the exact revision. Stricter pod
readiness must be verified against actual actuator lifecycle behavior. A process exit cannot
replace host-retained shutdown-cause evidence, nor can an invalid fixture prove live rejection.
Task ownership, validation results and remaining work live in the
[VM tracker](../../tasks.md#vm-test-coverage), including caller migration and evidence corrections.
