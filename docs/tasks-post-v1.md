# Post-v1 Backlog

Work that is real and intended, but deliberately not part of v1.

This file exists so [tasks.md](tasks.md) can answer exactly one question — what is left before v1 —
without carrying items that cannot close no matter how much work is done. Everything here is
tracked, not dropped: a decision recorded in `docs/contributing/design/scope-boundaries.md` or an upstream
dependency outside this project is what puts an item on this page.

The bar for moving something here is narrow. "Hard", "unscheduled", or "nobody has picked it up"
are not reasons — those stay in `tasks.md` as open v1 work. The two qualifying reasons are:

- **Scope**, where `scope-boundaries.md` places the work beyond v1. Reversing that means reopening
  the decision, not quietly re-planning.
- **Upstream**, where the capability does not exist yet in software this project consumes. No amount
  of work here closes it.

Last reviewed: 2026-08-14

---

## Inventory System

### `carries`-based ordering (`PL-21`)

Communication-path edges are compiled and published; nothing orders against them. Wiring the
ordering needs a network device to be an actuation target.

**Why it is post-v1:** `OD-24` makes switches topological-only, and the revisit condition is PDU
outlet control — both already on this page. The Planning & Execution Logic entry for `PL-21` is the
owning line; this is the Inventory-side half of the same gate.

### NetBox topology provider (`SB-8`)

The provider interface exists with the declarative CRD implementation behind it. NetBox is the
second implementation.

**Why it is post-v1:** `SB-8` states that the default build ships without NetBox, so no shippable
configuration depends on this, and the work is an integration against an external system this
project does not own. Nothing in v1 closes if it lands, and nothing in v1 is blocked while it does
not.

---

## Capability Profiles

### Actuation verification lifecycle (`F-27`)

Nothing defines what verification of a profile's declared actuation behaviors consists of, where the
result is recorded, or how it becomes a profile change
([quirks-aliasing-firmware.md](contributing/audits/quirks-aliasing-firmware.md)).

**Why it is post-v1:** the audit's own recommended order puts `F-27` before instant-command work
under `OD-20`, and `OD-20` is on this page. `UPSCapabilityProfile.spec.actuation.behaviors` has no
consumer until it lands, so there is nothing in v1 for a verification lifecycle to gate.

### Non-NUT power device actuation (`OD-24`)

Power devices with no NUT driver — UniFi RPS-class hardware and similar — are modeled
topologically today: they appear in inventory and shape shutdown ordering, but the operator never
actuates them. Making them actuatable means a second actuation path alongside NUT.

**Why it is post-v1:** `SB-2a` makes NUT the power-state path unconditionally, and a second
actuation surface is precisely the exception that rule exists to prevent. `OD-24` is recorded as v2
scoping and is explicitly decided alongside `OD-10` (USB and serial support), because both are
control surfaces outside the NUT-network-only posture that the security narrative in
`docs/concepts/architecture.md` and `docs/reference/images.md` rests on.

Reversing this is a scope decision, not a planning one.

### PDU outlet control (`OD-25` actuation half)

The PDU capability kind and its matcher path are v1 scaffolding and are built. Actually switching
outlets is not: it needs `OD-20` to settle which NUT instant commands enter scope, and it is the
same control-surface question as `OD-24`.

A profile can declare that a model has switchable outlets. Nothing acts on that declaration, and
`UPSCapabilityProfile.spec.actuation.behaviors` has the same dead-field shape until `OD-20` lands.

---

## Telemetry & Triggers

### NUT instant commands, including power cycling (`OD-20`)

`upscmd` and `upsrw` expose instant commands and writable variables — `shutdown.return`,
`load.off`, `load.on`, outlet cycling. `shutdown.return` is the interesting one: it stops the UPS
discharging into a dead load once the cluster is down, and restores power automatically when line
power returns.

**Why it is post-v1:** the operator's own actuator already owns real shutdown for nodes and
workloads, so instant commands are redundant for anything running in the cluster. What is left is
the tail end after the operator has finished, plus non-cluster hardware sharing the UPS. That is a
real use case and not a v1 one.

It is also the riskiest surface in NUT — these commands cut power to equipment — so it is bounded
by `OD-1`: anything touching power *return* is recovery orchestration, which is out of project
scope entirely. A v2 design has to separate "stop wasting battery" from "bring things back up",
because only the first is ours.

### NUT forced-shutdown broadcast, FSD (`OD-19`)

The decision half of this was tracked separately under Capability Profiles; both halves live here
now, since the decision cannot land before the implementation it gates.

NUT's native FSD broadcast is the conventional way a primary tells its secondaries to shut down.
The operator uses its own signal file instead, which is deliberate: the signal file carries flow
identity, a timestamp, and a reason, and is staleness-checked on read. FSD carries none of that.

**Why it is post-v1:** adopting FSD as an *additional* release signal would make shutdown
observable through standard NUT tooling, which has real operational value. It is not required for
correctness, and two release paths need a decision about which one wins before either is wired.

---

## Node Agent / DaemonSet

### `MONITOR` power value and `MINSUPPLIES` (`F-45`)

Every agent renders `MONITOR ... 1 ... secondary` with `MINSUPPLIES 1`, so a host fed by two UPS
devices shuts down when either one goes critical rather than when it actually loses its supplies
([nut-usage-audit.md](contributing/audits/nut-usage-audit.md)).

**Why it is post-v1:** `MINSUPPLIES` governs one host's own supplies and reaches nothing but
`upsmon`'s local `SHUTDOWNCMD` decision. `OD-37` locks that path down for v1 — the operator path
plans from inventory, which already models a node in more than one power domain (`IN-11`). The
hardcoded values are inert while the scaffold is disabled, and become real again only if it is ever
unlocked.

### USB and serial UPS support (`OD-10`)

Local USB and serial UPS connectivity is excluded from v1 by `RB-1`. Only network-reachable devices
are supported, and the driver allowlist enforces it.

**Why it is post-v1:** this is load-bearing for the security posture, not an oversight. The absence
of host device mounts, host device access, and privileged mode in `docs/reference/images.md` and
`docs/concepts/architecture.md` is justified on the grounds that UPS reachability is network-only. Most
community NUT container images assume direct USB access and document privileged containers, which
is precisely what `RB-1` refuses.

`SB-4` already names the shape the eventual support must take: a third container or a separate
DaemonSet with its own isolated actuation boundary and its own security rationale. It cannot be a
flag on the existing agent.

### Older NUT and UPS support via a `dummy-ups` translation layer

The operands build NUT 2.8.5 from source (`OD-32`), and the API assumes a NUT new enough to carry
the driver and TLS behavior the operator renders. Hardware or sites pinned to an older NUT, or to a
UPS whose only viable driver is older, have no supported path today.

**Why it is post-v1:** `dummy-ups` in repeater mode already proxies an upstream NUT server — that
is how `spec.upstreamNUT` works, and it is how `F-22` was corrected. The same mechanism is the
natural translation layer for an older or non-conforming upstream: the operator talks to a modern
`upsd` it controls, and that server repeats from whatever the site actually runs.

Nothing about this is designed yet. It is recorded because the alternative — widening the operand
images or the driver allowlist to accommodate old versions — would undo `OD-32` and `RB-2`, and
that trade should be made deliberately rather than discovered.

---

## NUT Server / upsd

Both items below share one cause: mutual TLS between `upsmon` and `upsd` does not work on any
released NUT built against OpenSSL. `server/netssl.c` in 2.8.5 ends its OpenSSL initialization with
`SSL_CTX_set_verify(ssl_ctx, SSL_VERIFY_NONE, NULL)` and never loads a client CA, so the server
never requests a client certificate whatever the configuration says (`F-41`). The directives exist
and are documented; the implementation behind them landed after 2.8.5 and 2.8.6 is unreleased.

Server authentication and verified client connections both work today — see `F-39`/`F-40`. What is
missing is only the client proving its own identity.

### Client certificates for `upsmon`

The operator provisions no client identity, so `verifyClientCertificates` would lock out every
agent and correctly defaults to `false`. On the OpenSSL path this needs `CERTFILE` in `upsmon.conf`
and a working `CERTREQUEST`. On the NSS path it needs a certificate database, which the source
build exists to avoid (`OD-32`).

When it becomes buildable, the identity should be minted by the operator's own CA into a per-agent
Secret, matching `hack/webhook-cert.sh`. The cert-manager CSI driver is the wider ecosystem's answer
for per-pod identity and is rejected here for the same reason cert-manager itself was: it has
nothing to reconcile while the cluster is losing power.

### Per-server TLS posture via `CERTHOST`

`CERTVERIFY` and `FORCESSL` are process-global in `upsmon`, so an agent monitoring servers with
different `spec.tls.mode` values renders the weakest common posture and reports `NUTTLSDowngraded`.
`CERTHOST` would give each `MONITOR` line its own flags. Under OpenSSL it also lands after 2.8.5.

The v1 behavior is not silent: the downgrade sets a Degraded condition naming why, which is the
part that mattered.

---

## Moving an item back

An item returns to `tasks.md` when its gate is gone — a NUT release ships, or a scope decision is
formally reopened and re-recorded. Move the text back with the reason it was here removed, so the
tracker never carries a stale "post-v1" label for work that is now simply open.
