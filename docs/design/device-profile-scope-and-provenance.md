# Device Profile Scope, Provenance, and Non-NUT Power Devices

Status: working document, 2026-08-03. Records which device classes get capability profiles, how
user-contributed profiles survive upgrades, and how non-NUT power devices are handled.

Companion to `capability-profiles-and-upsd-config.md` and
`docs/audits/quirks-aliasing-firmware.md`.

## Profile scope by device class

The dividing line is **queried versus topological**.

- **Queried devices** report state the operator plans against, over a protocol with device-specific
  variation. That variation needs a declared profile.
- **Topological devices** matter for their position in the graph, not their behavior. A switch
  matters because it `carries` a NUT path; a power panel matters because it `feeds` something.
  Neither is queried. There is nothing to declare.

| Class | Profile? | Rationale |
| --- | --- | --- |
| UPS | Yes — `UPSCapabilityProfile` | Queried; telemetry and actuation vary by model and firmware |
| NUT-managed PDU | Yes — scaffolded, see below | Queried via `powerman-pdu`; switchable outlets are device-specific |
| Switch, router, transfer switch, power panel | No | Topological only; `PowerInfrastructure` inventory |
| Non-NUT power devices (RPS and similar) | No | Topological in v1; actuation deferred |

**Explicit non-goal: there is no switch profile.** Whether a switch stays powered during an outage
is a topology fact expressed by `feeds` and `carries` edges (IN-3), not a device capability. This is
recorded because the symmetry is tempting and wrong.

## PDU profiles — parallel kind, scaffolding only

`powerman-pdu` is already in the driver allowlist (RB-2). A NUT-managed PDU with switchable outlets
is genuinely queryable and has device-specific instant-command behavior, so it will need capability
declarations once instant commands enter scope under OD-20.

**Decision: a parallel profile kind, not an extension of `UPSCapabilityProfile`.** Reusing the UPS
kind would keep the CRD count lower but make the name progressively misleading and force
UPS-specific fields to be optional-and-ignored for PDUs. A separate kind keeps each schema honest.

**Scope for v1: scaffolding only.** The kind may exist with a minimal schema and a matcher path. It
is not a v1 feature, carries no bundled catalog entries, and no PDU actuation is implemented. The
purpose is to establish the shape so that PDU support is an additive change later rather than a
refactor of the UPS profile schema.

Shared machinery that should be factored rather than duplicated when this is built: the deterministic
precedence chain (RS-5), semver rules (SB-9), the universal floor concept (PL-33), and the
verification lifecycle once OD-23 resolves it.

## Non-NUT power devices — RPS and the second actuation path

UniFi RPS is the concrete case. Findings generalize to any power device with a control surface that
is not NUT.

**RPS is not a NUT device.** No known NUT driver covers it; it is managed through the UniFi Network
controller. This should be verified against current NUT driver lists before the position is treated
as settled.

**RPS is `PowerInfrastructure` on the `feeds` path.** The real chain is UPS → RPS → switch →
(`carries`) → node. RPS failure removes DC power from downstream switches, which removes the
communication path for every node behind them. It is exactly the class of intermediate device the
`carries` modeling exists to capture, and it belongs in the topology graph as a first-class vertex.

**The architectural collision.** RPS can power-cycle its outputs remotely through the UniFi API.
That is real actuation capability reachable over a non-NUT path, which SB-2a currently forecloses:
the operator never speaks to power hardware directly, all interaction is NUT-mediated, and a device
with no NUT driver is unsupported rather than a bypass candidate.

So RPS forces a choice SB-2a does not currently permit:

- **Topological only.** Model it, let it participate in `feeds` and `carries`, never actuate it.
  SB-2a holds unchanged.
- **Second actuation path.** Introduce a provider abstraction for non-NUT power devices. This is a
  genuine architectural expansion — it would need its own credential handling, its own capability
  declaration mechanism, and its own security narrative, and it would weaken the single-mediation
  argument that makes SB-2a simple to reason about.

**Decision for v1: topological only.** RPS is modeled and never actuated. SB-2a stands.

**Deferral: revisit alongside USB UPS support (OD-10).** Both questions are the same seam — power
devices whose control surface sits outside the current NUT-network-only posture. USB UPS requires a
new isolated actuation boundary and its own security rationale; non-NUT device actuation requires
the same. Deciding them together avoids two incompatible answers to one architectural question, and
avoids weakening RB-1 and SB-2a twice in separate increments.

## Profile provenance

Users should be able to contribute profiles, and consumers should be able to tell what a profile's
claims rest on.

**Proposed field: `provenance`**, with three values.

| Value | Meaning |
| --- | --- |
| `ProjectVerified` | Exercised against real hardware by the project maintainer |
| `Community` | Contributed and accepted into the catalog; not independently hardware-verified |
| `UserLocal` | Authored in-cluster by the operator's own user; never shipped |

This is a claim about **testing**, not expertise. `ProjectVerified` means someone ran the device;
it does not imply authority beyond that, and it is more than most device catalogs offer.

Realistic catalog shape: a small `ProjectVerified` set (currently the two UniFi UPS families), the
universal floor, and a growing `Community` set. The project cannot verify hardware it does not own,
and the provenance field is what makes that limitation legible rather than hidden.

`UserLocal` needs no maintainer action — it is simply what a user's own CRD profile is.

## Upgrade safety for user profiles

The mechanism already exists and should be documented as a guarantee.

- `UPSCapabilityProfile` is a CRD. User profiles are cluster objects the operator never writes to.
- Bundled catalog data ships inside the operator image. Different storage, no collision.
- Operator upgrades replace bundled data; they cannot modify or delete user CRs.
- Per the RS-5 precedence chain, within a specificity tier a **CRD-sourced profile outranks bundled
  data**. A user profile for a model the catalog also covers wins, with no need to fork or disable
  the catalog.

**This must be stated user-facing, not left as an internal resolution rule.** Someone deciding
whether to invest effort in authoring a profile needs to know it survives upgrades and takes
precedence. FAQ entry drafted below.

## FAQ entry

Incorporated into `faq.md`; the version there is the user-facing copy.

> **Can I add my own capability profiles, and will an upgrade overwrite them?**
>
> Yes, and no.
>
> Capability profiles are `UPSCapabilityProfile` custom resources. Profiles you create live in your
> cluster as CRs; the profiles that ship with the operator live inside the operator image. Upgrading
> the operator replaces the bundled set and never touches your resources.
>
> Where both cover the same device, yours wins. Profile matching walks a fixed precedence chain —
> exact model and firmware, then exact model, then model glob, then driver family, then the
> universal floor — and within any tier, a profile you supplied outranks a bundled one. You do not
> need to fork the catalog or disable bundled profiles to override one.
>
> The bundled catalog is deliberately small. Profiles marked `ProjectVerified` have been exercised
> against real hardware; the project cannot verify devices it does not own. If you have a device
> that is not covered, a contributed profile is welcome.

## Open decisions

**OD-24 · Non-NUT power device actuation.** Whether a second actuation path is introduced for power
devices without NUT drivers, or whether they remain permanently topological. Deferred to be decided
alongside OD-10 (USB and serial UPS support), since both concern control surfaces outside the
NUT-network-only posture of RB-1 and SB-2a.

**OD-25 · PDU profile kind.** Schema shape for the parallel PDU capability kind, and which machinery
is factored out of `UPSCapabilityProfile` for shared use. Scaffolding only in v1.

**OD-26 · Provenance field semantics.** Whether `provenance` is advisory metadata or affects
resolution — for example, whether a `Community` profile should require explicit opt-in, or emit a
warning condition when matched.
