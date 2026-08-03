# NUT Usage and Fidelity Audit

Status: audit record, 2026-08-03, against commit `00eb3c0`. Static reading only; no live NUT
session tested.

Scope: cross-component. Which NUT mechanisms the system uses, which it declines, and whether the
layering stays upstream-loyal with enhancements stacked on top rather than replacing NUT's own
machinery.

This audit spans three separate components, and the distinction matters:

| Component | Runs | Rendered by | Role |
| --- | --- | --- | --- |
| `NUTServer` | `upsd` | `nutserver_render.go` | Serves the NUT protocol on TCP 3493 |
| `NodePowerAgent` | `upsmon` + actuator | `nodepoweragent_render.go` | Client of `upsd`; host-level actuation |
| Operator | `internal/nut/client.go` | — | Also a client of `upsd`, for telemetry |

Per-component findings live in `nutserver-pod-audit.md` and `node-agent-daemonset-audit.md`.

Findings use the shared `F-n` namespace.

## What is used correctly

**Protocol fidelity.** `internal/nut/client.go` speaks the real protocol — `LIST VAR` with
`BEGIN LIST VAR` / `END LIST VAR` framing and `ERR` handling — rather than shelling out to `upsc`.

**Variable coverage is standard-conformant.** `ups.status`, `battery.charge`, `battery.runtime`,
`battery.voltage`, `battery.low`, `input.voltage`, `input.frequency`, `input.transfer.high`,
`input.transfer.low`, `output.*`, `ups.load`, `ups.temperature`, `ups.mfr`, `ups.model`,
`ups.serial`, `ups.firmware`, `ups.type`, `ups.test.*`. Real NUT names throughout, no invented
schema.

**`MODE=netclient`** is correct for the network-only posture of RB-1.

**Every agent connects as `secondary`.** This is the SB-3 authority boundary expressed in NUT's own
vocabulary rather than bolted alongside it — no agent believes it owns the shutdown decision.

**`SHUTDOWNCMD "/bin/true"`** is the stub actuator expressed in NUT-native terms. NUT still runs its
complete state machine and still invokes the shutdown command; the command is simply inert.
Enabling real actuation becomes a config change rather than a control-flow change. This is the
clearest example in the codebase of the intended layering.

**`upsmon.conf` keyword set is correct and exposed as CR fields** rather than hardcoded:
`MINSUPPLIES`, `POLLFREQ` (15s), `POLLFREQALERT` (5s), `HOSTSYNC` (15s), `DEADTIME` (45s),
`POWERDOWNFLAG`, `FINALDELAY` (10s).

## Findings

**F-20 · `FSD` is referenced but the forced-shutdown path is not exercised.** FSD is NUT's mechanism
for a primary to declare a forced shutdown that all secondaries observe and act on. The current
architecture reaches the same outcome through the executor and signal file instead.

That is a legitimate choice — the operator needs ordering NUT cannot express — but it leaves the
NUT-native broadcast path sitting unused beside a parallel mechanism. Either document that FSD is
deliberately declined and why, or adopt it as the final release signal so shutdown is observable
through standard NUT tooling. Ambiguity here invites a future contributor to wire it up in parallel
with the executor.

**F-21 · `upssched` is not used at all.** NUT's own timer and event scheduler handles delayed
actions, escalation on sustained conditions, and per-event command dispatch. The operator
reimplements equivalent logic in Go.

Defensible: the operator needs cluster-wide correlation and dependency ordering that `upssched`
cannot provide, and centralizing decisions serves planner determinism. But it should be recorded as
a decision rather than left as an omission — "we use NUT fully" and "we decline NUT's scheduler"
need reconciling in the same document.

**F-22 · Instant commands and writable variables are entirely unused.** No `upscmd`, `upsrw`, or
`INSTCMD` anywhere in the codebase. This is the whole SB-2c capability surface: `load.off`,
`load.on`, `shutdown.return`, `shutdown.stayoff`, `beeper.mute`, `test.battery.start`, and writable
settings including `ups.delay.shutdown` and `ups.delay.start`.

The consequence with the clearest operational cost: **no `shutdown.return` handshake.** The
canonical NUT ending is that the last secondary disconnects, the primary commands the UPS to cut
power after a delay, and the UPS restores power when line power returns. Without it, nodes power
off while the UPS continues draining battery into a load that is no longer doing anything.

Second: **no battery self-test invocation.** `ups.test.result` is already read, but
`test.battery.start` is not exposed. Battery health determines whether the runtime figures the
planner depends on are trustworthy at all.

Support for these varies sharply by device and driver, so they belong in the capability profile
actuation section per CR-2 — worth recording there even before any of them are implemented.

Noted and explicitly out of scope: `ups.delay.start` and the power-return cycle touch recovery,
which OD-1 closed. Recorded here only so the capability surface is complete, not as a reopening.

*Corrected 2026-08-03: this finding overstated the unused surface. The `dummy-ups` repeater/relay
path is implemented and upstream-loyal (`docs/design/upstream-nut-relay.md`). F-22 stands only for
instant commands and writable variables. See the correction in `quirks-aliasing-firmware.md`.*

**F-24 · Credential handling: correct validation, plaintext in a rendered Secret.** Config values
are validated against injection before being written, which is good. The rendered `upsmon.conf`
carries the monitor password in cleartext inside a Kubernetes Secret — conventional and unavoidable
given NUT's config format. Two things to confirm rather than assume: that the Secret is never
logged or echoed into events on render failure, and that the config hash in
`ManagedResourceStatus` cannot leak the value.

## Modularity assessment

The layering is sound. NUT is not forked, not patched, and not bypassed at the protocol level. The
operator generates standard config, speaks the standard protocol, and expresses its own authority
model inside NUT's vocabulary.

One architectural asymmetry is worth stating explicitly in the design docs: the **decision** layer
sits entirely above NUT — no FSD, no `upssched` — while the **observation** layer sits inside it.
That is the right split for this project, but the current documents describe NUT as an abstraction
and event source without saying which of NUT's own control mechanisms are deliberately declined.

## Recommended order

1. F-22 record the instant-command surface in capability profiles, ahead of implementation.
2. F-20 and F-21 write the FSD and `upssched` decisions down explicitly.
3. F-24 confirm no credential leakage through logs, events, or hashes.

## New open decisions proposed

**OD-19 · FSD usage.** Whether NUT's forced-shutdown broadcast becomes the final release signal or
is deliberately declined in favor of the executor's signal file. Affects whether shutdown is
observable through standard NUT tooling.

**OD-20 · Instant command scope and gating.** Which commands enter scope, how they are gated given
they can cut power to equipment, and which capability profile fields declare support. Bounded by
OD-1 on anything touching power-return.
