# Quirk Handling, Variable Aliasing, and Firmware Gating

Status: findings record, 2026-08-03, against commit `00eb3c0`. Static reading only; no live device
tested.

Components: Capability Profiles.

Investigates three gaps surfaced by the recorded UniFi UPS quirks in
`docs/design/capability-profiles.md`. Findings continue the shared `F-n` namespace from F-24.

## Correction to an earlier finding

**F-22 was overstated.** `nut-usage-audit.md` reported that NUT's feature surface beyond basic
telemetry was unused. That is wrong in one respect: `dummy-ups` repeater mode is implemented and
documented in `docs/design/upstream-nut-relay.md`, including `repeater_disable_strict_start`
handling and a citation to the upstream man page. Appliances that expose their own `upsd` are
modeled as `UPSDevice.spec.upstreamNUT` and relayed.

F-22 stands only for **instant commands and writable variables** — `upscmd`, `upsrw`, `INSTCMD`.
The relay path is real, correct, and upstream-loyal.

## Recorded quirks — the existing registry

Two real catalog profiles exist for product families with built-in NUT servers:
`ubiquiti-unifi-ups-tower` and `ubiquiti-unifi-ups-2u`. Recorded quirks:

- The devices expose a built-in NUT server rather than SNMP.
- Firmware before `1.4.18` had reported NUT protocol response bugs.
- Credentialed reads may require normal NUT client configuration rather than ad-hoc `upsc` flags.
- The devices report `battery.low`, where the standard NUT name is `battery.charge.low`.
- Tower output power and current telemetry may vary by firmware or load until tested against real
  units.

Actuation behaviors are deliberately declared empty because `upscmd` behavior changed across early
firmware. This verification-gated posture is correct and should be preserved.

## Findings

**F-25 is fixed (2026-08-05).** Alias maps are part of the profile telemetry section, applied by the
normalizer, and both applied and shadowed aliases surface as telemetry diagnostics. OD-23's
precedence question is closed with the rules in `capability-profiles.md`. The `battery.low` quirk
that motivated this finding now has a real alias behind it. The original finding follows.

**F-26's firmware-scoping question remains open**, but the two bundled quirks that field testing
disproved (`built-in-nut-server`, `snmp-not-supported-by-ups`) were corrected to firmware-scoped
statements of what was actually observed, following the `firmware-before-1.4.18` precedent already in
the catalog. That is a data correction, not an answer to OD-22.

**F-25 · Variable aliasing has no mechanism, and the quirk that needs it is already recorded.**
`internal/telemetry/normalize.go` reads NUT variables by literal name and does not consume
capability profiles at all — no profile, match result, or alias map appears in the package. The
`battery.low` versus `battery.charge.low` deviation is recorded as prose in a quirk string, where
nothing can act on it.

Consequence: on a UniFi device, a normalizer looking for `battery.charge.low` finds nothing. The
value is preserved in the raw `Variables` map, so no data is lost, but no derived field or trigger
sees it. Combined with PL-19 trigger-capability validation, this is the silent-failure class OD-9
exists to prevent — a low-battery condition that never resolves, discovered during an outage.

**Answer to the design question: yes, aliasing should be supplied through profiles.** It belongs
there for the same reason the rest of the telemetry section does — it is a property of the device
model, not of the deployment, and it varies by firmware in exactly the way profiles already version.

Proposed shape, as a schema question rather than a settled design: extend
`UPSCapabilityTelemetrySpec` from a bare `variables []string` to also carry an alias mapping from
device-reported name to canonical NUT name. The resolver applies the alias when constructing the
telemetry snapshot, so the planner and trigger evaluator only ever see canonical names.

Three constraints on that design:

- Aliasing must happen in the **resolver**, not the planner. Per PL-1 through PL-4 the planner
  performs no I/O and receives resolved inputs; the alias is part of resolution.
- The alias must be recorded in the telemetry snapshot's diagnostics, so an operator can see that a
  value arrived under a non-standard name rather than silently receiving a canonical one.
- Aliasing must be one-directional and total. Two device names mapping to one canonical name, or a
  canonical name that is both aliased and natively present, need a defined precedence — the same
  determinism discipline as RS-5.

**F-26 · Firmware-gated quirks cannot expire.** `Quirks` is `[]string` — a flat, unstructured list.
There is no way to express "applies to firmware below 1.4.18." A device running current firmware
inherits every historical quirk of its model family permanently.

The selector already supports firmware narrowing (`selector.firmware` for an exact match), so the
matching layer has firmware awareness that the quirk layer does not. Two candidate resolutions:

- **Structured quirks.** Promote `Quirks` from `[]string` to a list of objects carrying an
  identifier plus an optional firmware constraint. Keeps one profile per model family.
- **Firmware-ranged profiles.** Extend `selector.firmware` from exact match to a range or
  constraint expression, and split affected families into version-scoped profiles.

The first is less disruptive and keeps profile count low. The second reuses the existing precedence
chain rather than adding a second conditional-evaluation site. Not resolved here; tracked as OD-22.

Note this interacts with SB-9's versioning rule. A quirk that expires is not a profile behavior
change and should not force a MAJOR bump under the correction-reduces-capability rule — the
capability did not change, the device did.

**F-27 · Verification-gated actuation has no lifecycle.** The UniFi profiles declare no actuation
behaviors pending firmware verification. This is the right default, but nothing defines:

- What constitutes verification — which commands are exercised, against which firmware, with what
  evidence.
- Where the result is recorded. OD-15 established `capability_profile_verifications` in PostgreSQL
  for probe history, but that table tracks telemetry probes, not actuation verification.
- How a verified result becomes a profile change. Presumably a human edit and a version bump, but
  the path from "someone tested it" to "the catalog says it works" is unwritten.
- Whether a user who has verified a device locally can declare support without a catalog release.
  Per RS-5's precedence chain a CRD profile outranks bundled data, so the mechanism exists; the
  guidance does not.

Actuation verification carries real risk — the commands under test can cut power to equipment — so
the absence of a defined procedure is more consequential here than for telemetry probing.

## Recommended order

1. F-25 alias mechanism. A recorded quirk currently has no path to affect behavior, and the failure
   mode is silent and outage-timed.
2. F-26 firmware gating. Decide between structured quirks and ranged selectors before the catalog
   accumulates more entries.
3. F-27 verification lifecycle. Needed before any instant-command work under OD-20.

## New open decisions proposed

**OD-22 · Firmware-conditional quirks.** Structured quirk objects with firmware constraints, versus
firmware-ranged selectors and version-scoped profiles. Determines whether conditional evaluation
happens in one place or two.

**OD-23 · Telemetry variable aliasing.** Whether alias mappings live in the profile telemetry
section, and how collisions and precedence resolve when a canonical name is both aliased and
natively reported.
