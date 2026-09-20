# Hadron VM-4: two-node topology, real drain/eviction, and network-policy enforcement

Status: in progress, 37 live runs across two workflows, 2026-09-18/20. Cross-node networking root
cause resolved 2026-09-20 (network-policy milestone passing live; drain milestone's own core
mechanism proven). The `waitForExactlyOneRunningAgentPod` timeout that blocked the drain
milestone's final assertions is diagnosed and fixed (`483ba7c`). Re-verifying that fix live is now
blocked on an unrelated new issue in a different, just-added file (see "Open"). See
`docs/tasks.md`'s `VM-4` entry for current status.

## Scope

VM-4's own remaining text after its third (single-guest) milestone closed: "The two-guest
topology, real drain/eviction against a live workload Pod, and the network-policy/audit
assertions remain open." Two milestones close this:

- `TestHadronOutageFlowTwoNodeDrainsWorkload` (`test/hadron/outage_flow_two_node_smoke_test.go`,
  `hadron-outage-flow-two-node-smoke.yml`): a real k3s server/agent pair (reusing VM-2's own
  now-closed join), the manager/PostgreSQL/simulated UPS pinned to the server (the survivor), a
  real workload Pod on the agent, and a real `ShutdownFlow` whose `DrainNodes` step (tier 2) really
  cordons and evicts that Pod before its `AgentShutdown` step (tier 1, `Simulate`) proceeds.
- `TestHadronOutageFlowTwoNodeEnforcesNetworkPolicy`
  (`test/hadron/outage_flow_two_node_network_policy_smoke_test.go`,
  `hadron-outage-flow-two-node-network-policy-smoke.yml`): a minimal two-node fixture (manager +
  NUTServer only) proving the real rendered NetworkPolicy
  (`internal/controller/nutserver_networkpolicy.go`) permits a same-namespace probe pod on the
  agent node while denying an unrelated-namespace one, cross-node, against the same live upsd port.

Both share `bootAndJoinTwoNodeCluster`, so a fix to the join/networking layer benefits both.

## Root cause chain

Five real, distinct bugs found and fixed in sequence, each confirmed by a live run's own evidence
before being called a fix -- not guessed:

1. **`applyWorkloadDeploymentOnNode`'s own readiness check** (`fabd3ba`) used
   `kubectl get pods -o jsonpath={.items[0]...}` inside a retry loop; the very first check (before
   any Pod existed) hit the jsonpath engine's own "array index out of bounds" error on the empty
   list, and `runKubectlOutput` turns any kubectl error into an immediate `t.Fatalf` -- aborting the
   whole test on a normal, momentary state instead of retrying. Switched to a typed
   `clientset.CoreV1().Pods().List`, the same pattern every other wait in this package already uses.
2. **A wrong Deployment name** (`8a42982`) in the network-policy test's own "NUTServer Ready"
   check: it queried the bare NUTServer name, but `deploymentName(server) = server.Name +
   "-nut-server"`. The check always got `NotFound`; the pod itself was already `2/2 Running` the
   whole time, visible in that same check's own diagnostic dump.
3. **The real root cause: Flannel's own VXLAN tunnel endpoint (VTEP) never followed node-ip.**
   k3s auto-selects each node's own `node-ip` (and, independently, Flannel's own
   `flannel.alpha.coreos.com/public-ip` node annotation) by default-route reachability when neither
   is set explicitly. Neither `test/hadron/network.go`'s `ClusterLink` wiring nor
   `cluster_join_smoke_test.go`'s own join ever set either, so both nodes registered the shared,
   isolated per-guest NAT address (`10.0.2.15`, identical on both guests) instead of their own
   distinct `ClusterLink` address -- confirmed directly via `kubectl get nodes -o wide`'s
   `INTERNAL-IP` column showing `10.0.2.15` on both nodes (run 35377319175/35377320698).
   `pinK3sNodeIP` (`f23770b`) patches `/etc/rancher/k3s/config.yaml` with the correct `node-ip` and
   restarts the service after each guest's `ClusterLink` address is assigned. Two follow-on bugs in
   that fix itself: a plain k3s-agent install has no reason to have already created
   `/etc/rancher/k3s/` (`38169bb`, `mkdir -p` first), and the agent has no local kubeconfig at all
   (`0ff71a3` -- `k3s kubectl` there falls back to the ancient insecure `localhost:8080` default and
   always fails with connection refused, unrelated to the real fix; the follow-up
   `waitForAgentServiceRouting` check reads the ClusterIP from the server instead).
   But **`node-ip` alone was not enough**: even after it was confirmed correct
   (`kubectl get nodes -o wide` showing distinct `192.168.100.1`/`.2`, run 35407386941 onward),
   real cross-node pod/Service traffic kept failing with `nc: Host is unreachable`. A full live
   iptables dump (`93a15f6`, `b4acea8`) showed the `KUBE-SERVICES -> KUBE-SVC -> KUBE-SEP -> DNAT`
   chain was already completely correct, and a real Endpoints check (`843e1d2`) showed a real ready
   backend -- ruling out kube-proxy and "zero ready endpoints" as the cause. `kubernetes.default`'s
   own Endpoints point directly at the API server's real host address (not an overlay pod IP), so
   the join's own `waitForAgentServiceRouting` check had only ever proven `ClusterLink`
   host-to-host reachability (already known to work), never Flannel's actual VXLAN overlay between
   the two nodes. Checking Flannel's own `flannel.alpha.coreos.com/public-ip`/`backend-data`
   annotations directly (`93bbccd`) found the smoking gun: **both nodes' own public-ip annotation
   was still `10.0.2.15`** -- Flannel's VTEP announcement is independent of `node-ip` and does not
   get re-derived by a mere process restart (most likely sticky once written at first
   registration). `9f775ee` patches this annotation directly via `kubectl annotate --overwrite`
   after both nodes join.

## Evidence

| Run(s) | Commit | Result | Finding |
| --- | --- | --- | --- |
| [35365755225](https://github.com/MichaelZalud18/nut-operator/actions/runs/35365755225) | `cf59965` | fail | First-ever live run of this milestone. Failed at `applyWorkloadDeploymentOnNode`'s own jsonpath bug (finding 1 above). |
| [35377319175](https://github.com/MichaelZalud18/nut-operator/actions/runs/35377319175) / [...320698](https://github.com/MichaelZalud18/nut-operator/actions/runs/35377320698) | `fabd3ba` | fail | Workload-pod fix confirmed (progressed past it). New: NodePowerAgent/NUTServer-Ready timeouts with no diagnostic evidence at all. |
| [35387633739](https://github.com/MichaelZalud18/nut-operator/actions/runs/35387633739) / [...635461](https://github.com/MichaelZalud18/nut-operator/actions/runs/35387635461) | `8a42982` | fail | Added diagnostics. Found finding 2 (wrong Deployment name) directly from the diagnostic's own pod dump. |
| [35389867877](https://github.com/MichaelZalud18/nut-operator/actions/runs/35389867877) / [...870118](https://github.com/MichaelZalud18/nut-operator/actions/runs/35389870118) | `ab0fb5d` | fail | Node-name fix confirmed. Added node `INTERNAL-IP` capture -- found both nodes reporting the identical shared NAT address `10.0.2.15` (finding 3, part 1). |
| [35392170871](https://github.com/MichaelZalud18/nut-operator/actions/runs/35392170871) / [...172793](https://github.com/MichaelZalud18/nut-operator/actions/runs/35392172793) | `f23770b` | fail | `pinK3sNodeIP` introduced. New: agent's own write step failed -- `/etc/rancher/k3s/` did not exist yet. |
| [35407386941](https://github.com/MichaelZalud18/nut-operator/actions/runs/35407386941) / [...387947](https://github.com/MichaelZalud18/nut-operator/actions/runs/35407387947) | `38169bb` | fail | `mkdir -p` fix confirmed -- node-ip now correct (`192.168.100.1`/`.2`). New: `waitForAgentServiceRouting`'s own check failed (a plain agent has no local kubeconfig). |
| [35409305442](https://github.com/MichaelZalud18/nut-operator/actions/runs/35409305442) / [...306374](https://github.com/MichaelZalud18/nut-operator/actions/runs/35409306374) | `430cd6a` | fail | Added the kube-proxy-catch-up wait. Still hit the no-local-kubeconfig bug in that same new check. |
| [35411593912](https://github.com/MichaelZalud18/nut-operator/actions/runs/35411593912) / [...595587](https://github.com/MichaelZalud18/nut-operator/actions/runs/35411595587) | `0ff71a3` | fail | Fixed to read the ClusterIP from the server. Real cross-node traffic still failed: `nc: Host is unreachable`, ruling node-ip alone insufficient. |
| [35454896041](https://github.com/MichaelZalud18/nut-operator/actions/runs/35454896041) / [...897228](https://github.com/MichaelZalud18/nut-operator/actions/runs/35454897228) | `93a15f6` | fail | Added iptables/journal capture. Grepped by ClusterIP -- showed only the `KUBE-SERVICES`/MASQ lines (`KUBE-SEP-*` rules don't reference the ClusterIP, a blind spot found later). |
| [35456619496](https://github.com/MichaelZalud18/nut-operator/actions/runs/35456619496) / [...620511](https://github.com/MichaelZalud18/nut-operator/actions/runs/35456620511) | `843e1d2` | fail | Added a direct Endpoints check -- a real, ready backend existed. Ruled out "zero ready endpoints." |
| [35458246478](https://github.com/MichaelZalud18/nut-operator/actions/runs/35458246478) / [...247399](https://github.com/MichaelZalud18/nut-operator/actions/runs/35458247399) | `b4acea8` | fail | The drain test's own grep-by-name iptables dump showed the *complete* `KUBE-SERVICES -> KUBE-SVC -> KUBE-SEP -> DNAT` chain, fully correct. Added Flannel `subnet.env`/route-table capture. |
| [35460004672](https://github.com/MichaelZalud18/nut-operator/actions/runs/35460004672) / [...005600](https://github.com/MichaelZalud18/nut-operator/actions/runs/35460005600) | `93bbccd` | fail | Added Flannel `public-ip`/`backend-data` annotation capture. **Found the smoking gun**: both nodes' own public-ip annotation was still `10.0.2.15`. |
| [35461645908](https://github.com/MichaelZalud18/nut-operator/actions/runs/35461645908) / [...647008](https://github.com/MichaelZalud18/nut-operator/actions/runs/35461647008) | `9f775ee` | fail | Patched the annotation directly. Drain run reached the fix; netpol run hit an unrelated SSH flake before reaching it (inconclusive that round). |
| [35462958178](https://github.com/MichaelZalud18/nut-operator/actions/runs/35462958178) | `d973e3b` | fail | **Confirmed the Flannel fix works**: both nodes' annotations now show correct, distinct addresses. Failure mode changed from `Host is unreachable` (routing) to `upsmon` alternating `Connection refused`/`Operation timed out` (real network-layer responses). Added upsmon's own container log capture. |
| [35464505198](https://github.com/MichaelZalud18/nut-operator/actions/runs/35464505198) / [...505954](https://github.com/MichaelZalud18/nut-operator/actions/runs/35464505954) | `a658a90` | fail | Same improved failure mode confirmed on the netpol side too (`Operation timed out`, not unreachable). Attempted an `ipset list` dump to check kube-router's own NetworkPolicy set membership -- failed: `ipset: command not found` on the guest. |
| [35471537511](https://github.com/MichaelZalud18/nut-operator/actions/runs/35471537511) / [...538861](https://github.com/MichaelZalud18/nut-operator/actions/runs/35471538861) | `8dd4d2d` | fail | Widened both post-fix wait budgets (3m→6m, 2m→5m) to test whether this was a timing/sync-lag gap. **Disproven**: every retry across the full widened budget failed identically -- not intermittent. |
| [35472978358](https://github.com/MichaelZalud18/nut-operator/actions/runs/35472978358) | `1ed8d80` | fail | Fixed the netpol test's own iptables grep to match by service name (the same blind spot fixed for the drain test). Showed the *complete* chain for this Service too: `KUBE-NWPLCY-*` (NetworkPolicy) and `KUBE-SERVICES -> KUBE-SVC -> KUBE-SEP -> DNAT` both fully correct and present. |
| [35476832000](https://github.com/MichaelZalud18/nut-operator/actions/runs/35476832000) / [...833025](https://github.com/MichaelZalud18/nut-operator/actions/runs/35476833025) | `4e5be15` | fail | Attempted `apt-get install ipset` on the agent first. Failed outright: `sudo: apt-get: command not found` -- this guest's own base OS has no package manager at all, not just a missing package (see below). Same `ipset: command not found` result as before. |
| [35526849636](https://github.com/MichaelZalud18/nut-operator/actions/runs/35526849636) / [...850709](https://github.com/MichaelZalud18/nut-operator/actions/runs/35526850709) | `b67884c` | fail | Corrected diagnostic tool paths (Codex's own finding) worked. `flannel.1` link dump directly showed `local 10.0.2.15 dev ens3` -- confirmed the annotation-only fix was incomplete. **Also settled the ipset question entirely**: `KUBE-SRC-*` (egress/source match, same-namespace selector) correctly contained the cross-node agent probe pod's own IP (`10.42.1.4`) alongside same-namespace pods, and `KUBE-DST-*` (ingress/destination match) correctly contained the NUTServer pod's own IP (`10.42.0.10`) -- ipset population is correct on both sides, ruling out the kube-router cross-node sync-gap theory outright. The entire remaining symptom is fully explained by the Flannel VXLAN local-binding bug alone. |

## Current state

Every layer that can be checked via `kubectl` and iptables text is confirmed structurally correct
end to end, for both milestones, on both the drain and network-policy fixtures, including ipset
membership itself (confirmed correct on both sides as of 2026-09-20, below). The isolated remaining
cause is Flannel's own local VXLAN device binding, also confirmed live as of 2026-09-20 and now
fixed pending live re-verification.

**Confirmed (2026-09-19): the host has no package manager.** Kairos's own Hadron base OS ships none
at all by design --
["Package Management: None by design... engineered from scratch... extend it using container
technologies, or simply use system extensions without ever touching a package
manager"](https://kairos.io/blog/2025/12/17/introducing-hadron-the-minimal-upstream-first-linux-base-for-kairos/).
A live `apt-get install ipset` attempt failed immediately with `apt-get: command not found` (run
35476832000/35476833025), confirming this directly rather than assuming it from the documentation
alone.

**Correction (2026-09-20):** the conclusion drawn from that finding -- that `ipset`/a real `ip`
were unavailable on the guest at all, requiring a privileged debug container -- was wrong. k3s
vendors its own copies of both at a fixed data path (`/var/lib/rancher/k3s/data/current/bin/`),
verified directly against the exact pinned k3s-root release archive's own packaging script and
checksum, not assumed; a plain `ip`/`ipset` invocation through sudo's own PATH never finds them.
Separately, this investigation's own `ip -d link show flannel.1` call silently carried no real
evidence at all: busybox's minimal `ip` doesn't support `-d` and printed its own usage text
instead of erroring, which looked like a successful, empty-ish result rather than a failed one.
Full evidence and a live-unconfirmed hypothesis that patching Flannel's public-ip *annotation*
does not by itself prove the local VXLAN device's own binding changed (annotations are read by
*peers*, not by the local node) are in
[hadron-vm-4-diagnostic-tools-2026-09-20.md](hadron-vm-4-diagnostic-tools-2026-09-20.md). Fixed:
`dumpKubeProxyState` now uses the real bundled tool paths, keeps each command's own success/failure
visible separately (a masked failure was itself part of the earlier mistake), and dumps ipset state
on both the agent and the server (an agent-only dump cannot show destination-side ingress
membership for the NUTServer pod, which lives on the server).

**Confirmed (2026-09-20): Codex's hypothesis was correct.** The corrected diagnostic's own live
`ip -d link show flannel.1` output was unambiguous:
`vxlan id 1 local 10.0.2.15 dev ens3 srcport 0 0 dstport 8472 ...`
(run 35526849636/35526850709) -- the device's own local source address and parent interface were
still the shared, isolated per-guest NAT address on the management NIC, not the ClusterLink
address, even after `node-ip` and the Flannel public-ip annotation were both already corrected.
The annotation only tells *other* nodes where to send traffic destined for this node; it never
rebinds this node's own local VXLAN source address, which Flannel derives independently and does
not automatically follow `node-ip`. Fixed: `pinK3sNodeIP` now also sets `flannel-iface` (using the
MAC-discovered `ClusterLink` interface `assignClusterLinkAddress` already returns for both the
server and the agent -- the server's own call previously discarded that return value) alongside
`node-ip`, before the same bounded restart. The Flannel annotation patch is left in place as a
belt-and-braces safety net; it should now be a no-op once Flannel self-derives the correct value
from the corrected interface binding. Not yet confirmed live. Investigation continues; not yet
closed.

**Confirmed live (2026-09-20), run 35529204930: `flannel-iface` is the fix.**
`TestHadronOutageFlowTwoNodeEnforcesNetworkPolicy` **passed completely** -- same-namespace probe
allowed, unrelated-namespace probe denied, cross-node, for the first time ever. The sibling drain
run (35529203632) got dramatically further too: NodePowerAgent reported Ready, PostgreSQL Ready,
real telemetry transitioned OnBattery, and `DrainNodes` **really cordoned the agent Node and really
evicted the workload Pod** -- the actual core mechanism this milestone exists to prove, working
end to end for the first time. It failed later and separately: `waitForExactlyOneRunningAgentPod`'s
own DaemonSet pod list call hit `context deadline exceeded` post-drain -- a new, much smaller, and
likely unrelated issue (possibly transient API churn right after cordoning the node it's listing
against). Not yet investigated. The cross-node networking root cause this whole document tracks is
resolved.

## Open

- **Diagnosed and fixed (2026-09-20, commit `483ba7c`):** the `waitForExactlyOneRunningAgentPod`
  timeout from run `35529203632` was not a networking issue. Its check closure passed the parent
  (already 2-minute-bound) `ctx` straight into `clientset.CoreV1().Pods(...).List(...)` with no
  per-attempt sub-context -- every other retry check in this package wraps its call with a fresh
  15s sub-context for exactly this reason (a single hung request can otherwise consume the whole
  retry budget before `pollGuest`'s 5s cadence gets a chance to retry). The log showed exactly
  2:00.00 elapsed with a single "context deadline exceeded" and no interleaved retry attempts,
  confirming this diagnosis. Fixed both `waitForExactlyOneRunningAgentPod`
  (`outage_flow_smoke_test.go`) and its sibling `waitForExactlyOneRunningAgentPodNamed`
  (`actuator_daemonset_smoke_test.go`), which had the identical bug.
- **New blocker found while re-verifying the fix above, run `35532067290` (2026-09-20) --
  unrelated to this document's networking investigation, not mine to fix:** the drain test now
  fails during agent VM creation with `Create (agent): QEMU does not reference the owned state
  directory`, from `test/internal/vmprocess/process_linux.go:51` (added in commit `047e917`,
  "fix(test): retain verified QEMU ownership through VM cleanup" -- not authored by this
  investigation). This is that file's first-ever live exercise. The server guest's own `Create()`
  call in the same run passed the identical ownership check successfully; only the agent's failed.
  I have not touched `process_linux.go`/`machine.go` and have not root-caused this further --
  it is Codex's own new file. Evidence for whoever picks it up: the check reads the QEMU process's
  `/proc/<pid>/cmdline` and requires an adjacent `-monitor`/`unix:<StateDir>/qemu-monitor.sock,server,nowait`
  argument pair matching `m.Config().StateDir` (`vmprocess/machine.go`'s `capture`); PEG
  (`pkg/machine/qemu.go:157`) builds the same string from `q.machineConfig.StateDir` via
  `path.Join`, which is byte-identical to `filepath.Join` on Linux, so a naive path-format
  mismatch looks unlikely from static reading alone -- this needs live diagnosis (e.g. dumping the
  agent's actual `/proc/<pid>/cmdline` and `m.Config().StateDir` at the failure point), not another
  static-code guess.
- Real audit-row assertions for the two-node drain flow (not yet attempted; `assertRealDrainAuditRecords`
  exists in the test but has not yet passed live) -- blocked on the new blocker above until the
  drain test can boot both guests again.
- Real actuation (`Actuate`/`PowerOff`) plus a survivor-availability assertion under an actual halt,
  deliberately deferred from this milestone's own scope (`Simulate` only, matching every other
  milestone's incremental-scope discipline).
