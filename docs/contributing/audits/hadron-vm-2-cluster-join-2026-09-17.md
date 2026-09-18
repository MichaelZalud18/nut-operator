# Hadron VM-2: the real two-node k3s join

Status: seven live runs, 2026-09-17/18. See `docs/tasks.md`'s completed-tasks entry for current
status.

## Scope

`TestHadronClusterJoin` (`test/hadron/cluster_join_smoke_test.go`,
`hadron-cluster-join-smoke.yml`), the remaining open piece of VM-2 after the earlier
[PEG evaluation](hadron-vm-2-peg-evaluation-2026-09-11.md) (2026-09-11) proved single-guest boot,
host-side kubeconfig access, and the raw point-to-point `ClusterLink` segment carrying traffic
between two independently booted guests. That work deliberately never installed or ran k3s at all,
isolating the socket-netdev link itself from everything else. This milestone proves the rest: a
real k3s server, a real k3s agent, and a genuine two-node join over that same link, confirmed by
listing two distinct Ready nodes through the server's forwarded kube-API from outside both guests
entirely.

The raw link has no DHCP; each guest's static address is assigned imperatively over SSH after boot
(`assignClusterLinkAddress`'s own doc comment explains why, given this project's one prior
experience with an apparently-documented but silently-non-firing Kairos cloud-config feature).
Sequencing is real, not a simplification: the agent's own cloud-config needs the server's
node-token, which the server does not generate until its own first boot completes.

## Evidence

| Run | Result | Finding |
| --- | --- | --- |
| [35284990119](https://github.com/MichaelZalud18/nut-operator/actions/runs/35284990119) | fail | Every harder-sequenced step succeeded (server boot/Ready, both static addresses assigned, node-token handoff, agent install/reboot/join attempt), but the agent's `k3s-agent` service never reached the server: repeated `failed to get CA certs: ... context deadline exceeded`, well after both addresses were already assigned -- not the addressing race, a persistent connectivity failure. |
| [35289561840](https://github.com/MichaelZalud18/nut-operator/actions/runs/35289561840) | fail | `docs.k3s.io/installation/requirements` documents this exact symptom's primary known cause: turn off firewalld/ufw. Added that as a cloud-init `runcmd` (plain, not the Kairos-specific stage this project already found silently does not fire) plus direct diagnostics. Result: firewalld/ufw were already `inactive` on both guests (the fix was a real no-op, cleanly ruling that theory out), and a raw curl to the server timed out at exactly the 5s budget (`http_code=000`) -- and the "is anything listening" check itself was uninformative: piped through `grep`, which discards all output and exits non-zero on zero matches, over an unconfirmed `ss` binary. |
| [35291722820](https://github.com/MichaelZalud18/nut-operator/actions/runs/35291722820) | fail | Hardened diagnostics (fallback across `ss`/`netstat`/`/proc/net/tcp`, no grep filter, "; true" everywhere) revealed the real finding: the agent's own `ens5` (its ClusterLink NIC) had vanished entirely from `ip -4 addr show` by the first diagnostic snapshot -- not link-down, absent from the listing altogether -- while the server's own `ens5` (identical mechanism, only the `Server()`/`Client()` socket-netdev role differs) stayed present and unchanged across every sample. |
| [35294117951](https://github.com/MichaelZalud18/nut-operator/actions/runs/35294117951) | fail | Added a check to catch the disappearance in the act, but built it on `waitForWithDiagnostics`, which returns on the check function's *first* success -- it proved the address existed at one instant, then moved on immediately, learning nothing about whether it survived. |
| [35297127114](https://github.com/MichaelZalud18/nut-operator/actions/runs/35297127114) | fail | Replaced with a real continuous poll. New failure shape this run: the check itself started erroring with `ssh: handshake failed: ... connection reset by peer`, not "address missing" -- a different symptom the tolerant-of-one-blip version couldn't yet separate from noise. |
| [35298330408](https://github.com/MichaelZalud18/nut-operator/actions/runs/35298330408) | fail | Confirmed the reset was a real, reproducible signal, not noise: `ssh: handshake failed: ... connection reset by peer` on the loopback hostfwd port itself -- the kind of reset a fresh post-reboot network stack sends for a stray packet, not a guest-internal symptom. The "k3s-agent unit exists" check this test used to leave the live installer environment only proves Kairos wrote that unit file; it never proved the guest had finished settling (cOS/Elemental installs can involve more than one internal boot stage, not confirmed against Kairos's own docs either way). |
| [35298330408 → next] | **pass** | Added a real stability wait (nine consecutive successful trivial SSH commands, 5s apart, resetting to zero on any drop) before this test ever touches the agent's network. The next run's own log confirms the theory directly: `SSH dropped after 4 consecutive successes: ssh: handshake failed: ... connection reset by peer`, the wait absorbed it and kept retrying, and everything after that point succeeded cleanly -- address assignment, the persistence check (zero further drops), and the final wait, ending in `confirmed two distinct Ready nodes: [kairos kairos-5f07]`. |

Six real, distinct bugs across six failing runs, each fixed from direct evidence rather than a
first plausible theory: a cold-cache-only container-ID parse bug (closed in VM-7's own milestone,
same session), a stale cleanup-script prefix, a genuinely inactive firewall ruled out rather than
assumed, a fragile diagnostic that discarded its own evidence, a check that only ever proved one
instant, and finally the real root cause -- the agent guest was not yet done settling when this
test assumed it was, and touching its network during that window is what caused everything
downstream to fail, from a vanished interface to a reset SSH handshake, depending on exactly when
in that window each attempt happened to land.

The join itself is now closed. `VM-2` as a whole is not: its own acceptance criteria include
refusing mutation on mismatched cluster/VM/node identity and proving concurrent-run isolation
(port collisions, partial/never-started VM cleanup, process ownership before deleting state), none
of which this milestone built or exercises -- `docs/contributing/audits/vm-test-research-2026-09-15.md`'s
own `VM-2` section states this explicitly as real, specific work "not to be closed as N/A." See
`docs/tasks.md`'s `VM-2` entry for the current, narrowed scope.

## Open, deliberately not attempted here

- `VM-8`'s shared guest/cluster fixture extraction remains deferred; this milestone's own helpers
  (`bootClusterJoinGuest`, `assignClusterLinkAddress`) are small and self-contained in the same
  form this project's other VM-2/VM-3/VM-7 work already treats as acceptable until a third guest
  adapter makes the overlap self-evident.
- The exact internal Kairos/cOS boot-stage mechanism the stability wait works around (whether it is
  a genuine second reboot, a network-manager settle window, or something else) was not identified
  further than "the guest is not immediately stable the instant the k3s-agent unit file exists" --
  the fix does not depend on knowing the precise mechanism, only on not assuming the earlier check
  already covers it.
- Mismatched cluster/VM/node identity refusal and concurrent-run isolation (the two items listed
  under VM-2's own "Open" checklist in `vm-test-research-2026-09-15.md`): not attempted here, real
  remaining work, not a rename of what this milestone already proved.
- `VM-4` (two-guest survivor/outage topology) was blocked on the join existing at all; that part is
  now unblocked, though VM-2's own identity/isolation checklist above is not a VM-4 prerequisite.
