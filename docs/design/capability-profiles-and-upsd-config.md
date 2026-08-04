# Capability Profiles and `upsd` Configuration

Status: working document, 2026-08-03. Records what capability profiles do and do not influence in
the `upsd` operand.

Components: Capability Profiles, NUT Server / upsd.

Written to close a recurring question before it recurs: if profiles describe the device, should
`upsd` be sized or shaped to match the device? The answer is no for sizing and yes for
configuration, and the reasons differ enough to be worth stating separately.

Companion to `scaling-and-sizing.md`, `docs/audits/nutserver-pod-audit.md`, and the capability
schema work (SB-9, CR-1 through CR-3).

## The rule

**Capability profiles influence `upsd` configuration. They never influence `upsd` sizing.**

Profiles describe device behavior. Sizing is driven by client count. These are independent axes,
and conflating them produces fields that look symmetric and carry no information.

## Why sizing does not vary by device

`upsd` is device-agnostic in its resource profile. It maintains a variable table and serves clients
over TCP.

- **Variable count is negligible.** A small desktop UPS exposing eight variables versus an
  enterprise unit exposing sixty is a difference of a few kilobytes. Both are noise against the
  container's baseline footprint.
- **There is no smaller `upsd` to deploy.** The daemon is already near its floor. A "lightweight
  mode" for small devices would save nothing measurable.
- **Resource consumption tracks client count, not device class.** Client count follows node count,
  which is unrelated to UPS capacity or feature richness.

Concrete consequence: a tiny desktop UPS and a large rack unit get the same `upsd` container with
the same requests and limits. The correct configuration is static resources sized for expected
client count, identical across every device.

**Do not add resource hints, sizing tiers, or footprint fields to the capability profile schema.**
They will feel symmetric with the rest of the profile and carry no signal. This is a deliberate
omission, not an oversight — recorded here so it is not "corrected" later.

Related and already recorded: VPA is inappropriate for `upsd` for independent reasons — flat
predictable load, and resizing requires a restart that drops every client session. See
`scaling-and-sizing.md`.

## What profiles should influence, and currently do not

These are legitimate per-device configuration inputs. None are wired to profiles today; driver
settings come from `UPSDevice` spec directly.

**Driver selection.** Which NUT network driver the device requires — `snmp-ups`, `netxml-ups`,
`powerman-pdu`, `apcupsd-ups`. The profile is the natural home for this knowledge, since it is a
property of the model, not of the individual deployment. Today the driver is specified per
`UPSDevice`, which means the same fact is re-entered for every device of the same model.

**Poll interval and polling behavior.** Some SNMP management cards degrade or drop responses under
aggressive polling. This is a genuine per-device quirk and precisely what the profile's actuation
and quirk section exists to capture. Currently there is no path for a profile to constrain polling.

**Driver-specific parameters.** MIB selection, SNMP version, retry counts, timeout values. All
model-dependent, all currently manual.

**Instant command support.** Already identified as F-22 in `docs/audits/nut-usage-audit.md`. Which of
`shutdown.return`, `load.off`, `load.on`, `test.battery.start`, and the writable delay variables the
device actually supports. Profile-declared per CR-2, and tracked as OD-20.

## Design consequence

The capability profile's influence on the `upsd` operand is **config rendering only**. A profile
should be able to change which driver runs, how it is parameterised, and how it is polled. It
should never change the pod's shape, replica count, resource allocation, or scheduling.

Stated as a boundary for the capability schema doc:

> Capability profiles render into NUT configuration. They do not render into Kubernetes pod
> specification.

## Open items this raises

**OD-21 · Driver configuration ownership.** Whether driver name, poll interval, and driver-specific
parameters move from `UPSDevice` spec into capability profiles, or remain in spec with profiles
supplying defaults and validation. Moving them removes per-device duplication; leaving them keeps
deployment-specific overrides simple. A hybrid — profile supplies the default, spec overrides — is
consistent with the precedence chain in RS-5 and is the likely answer.
