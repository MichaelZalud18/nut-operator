# Capability Profiles

Components: Capability Profiles.

Capability profiles are versioned operator artifacts. They declare what the resolver may assume
about a UPS after it has matched stable provider keys such as `ups.model`, optional firmware, and
driver family. Runtime probes can detect drift, but they do not rewrite the matched profile during a
power event.

Profiles ship from three sources:

- Bundled profiles in `internal/capability`.
- Installable project-maintained catalog manifests in `config/catalog/`.
- User-authored `UPSCapabilityProfile` CRDs.

CRD profiles can override bundled profiles within the same match tier. The precedence chain remains:
exact model and firmware, exact model, model glob, driver family, unidentified-device profile.

Capability profiles are product/SKU behavior records. They are real project catalog entries when
they live in `config/catalog/`. They do not name an owner's physical device, hostname, power domain,
rack placement, or threshold policy. Those are inventory concerns layered through `UPSDevice`,
`PowerInventoryNode`, `PowerInfrastructure`, and `PowerInventoryEdge`.

## Bundled Ubiquiti Profiles

The bundled Ubiquiti profiles are first-class, project-maintained capability profiles for UniFi UPS
product families with a built-in NUT server. They are real catalog profiles for those product
families, not user examples:

- `ubiquiti-unifi-ups-tower`
- `ubiquiti-unifi-ups-2u`

The Tower profile targets observed model strings like `TOWER_1000VA_230V` and expected North
American variants like `TOWER_1000VA_120V`. The 2U profile targets observed model strings like
`2U_1500VA_230V` and expected North American variants like `2U_1440VA_120V`.

These profiles intentionally declare no UPS actuation behaviors yet. Community reports have shown
`upscmd` and protocol behavior changing across early UniFi UPS firmware, so outlet or UPS shutdown
commands are not treated as supported until they are verified against current firmware.

They are not vendor-endorsed Ubiquiti artifacts. They are maintained by this project from public
product documentation, public NUT observations, and field testing.

Known quirks:

- The built-in NUT server is not reachable on firmware `1.6.1` (TCP 3493 refused). Earlier reports
  described these devices as exposing one instead of SNMP; that claim did not survive field testing,
  so it is now scoped to the firmware it was actually observed against. See Field Verification below.
- Only SNMPv3 is offered on firmware `1.6.1` — no v1/v2c community string.
- Firmware before `1.4.18` had reported NUT protocol response bugs.
- Credentialed reads may require normal NUT client configuration rather than ad-hoc `upsc` flags.
- The devices report `battery.low`; this is recorded as a quirk because the normal NUT low-charge
  name is `battery.charge.low`.
- Tower output power/current telemetry may vary by firmware or load until tested against real units.

## No Packaged Profile for Your Device?

Create a `UPSCapabilityProbe` naming the `UPSDevice`. The operator reads what the device actually
reports and writes a ready-to-apply profile into `status.draftProfile`, plus a GitHub-issue-formatted
`status.issueReport` so a verified profile can be contributed back to the catalog.

The draft declares only observed variable names — never readings, since a profile describes a product
model rather than one unit — and always leaves the actuation section empty, because actuation
commands can cut power and support is declared only after verification (F-27). Anything inferred
rather than observed, such as a suggested alias, is marked unverified in the generated YAML.

Probing is advisory (RS-7 through RS-10): it never changes how a device resolves, never feeds the
planner, and never runs on the failure path. See the FAQ entry "What if my UPS does not have a
packaged capability profile?" for the full walkthrough.

## Telemetry Variable Aliases

Some devices report a standard reading under a non-standard NUT name. The alias map in a profile's
telemetry section resolves that at normalization time, so the planner and trigger evaluator only ever
see canonical names:

```yaml
spec:
  telemetry:
    variables:
      - battery.charge.low
      - battery.low
      - ups.status
    aliases:
      battery.low: battery.charge.low
```

Aliases belong in the profile because they are a property of the device model, not the deployment
(OD-23, closed). Four rules make resolution deterministic:

- **Native readings win.** If the device reports the canonical name itself, the alias is not applied
  and the shadowed alias is recorded as a diagnostic. A profile can never mask a real reading.
- **Aliasing is one-directional and total.** A map key can only appear once, so a reported name
  resolving two ways is unrepresentable rather than merely invalid. Two reported names collapsing
  onto one canonical name is still possible to write, and is rejected at match time.
- **Nothing is discarded.** The reported name stays in the raw variable map alongside the canonical
  one.
- **Every applied alias is visible.** Resolution emits a telemetry diagnostic naming both the reported
  and canonical variable, so an operator can see that a value arrived under a non-standard name.

A profile is expected to declare the canonical name in `variables` as well as the alias. Trigger
support is derived from declared variables (CR-2), so an alias whose target is undeclared resolves at
runtime but contributes nothing to trigger validation, and raises a warning saying so.

## Influence on `upsd` Configuration

*Components: Capability Profiles, NUT Server / upsd.*

Closes a recurring question: if profiles describe the device, should `upsd` be sized or shaped
to match it? No for sizing, yes for configuration, and the reasons differ enough to state
separately. Companion to `scaling-and-sizing.md` and `docs/audits/nutserver-pod-audit.md`.

### The rule


**Capability profiles influence `upsd` configuration. They never influence `upsd` sizing.**

Profiles describe device behavior. Sizing is driven by client count. These are independent axes,
and conflating them produces fields that look symmetric and carry no information.

### Why sizing does not vary by device

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

### What profiles should influence, and currently do not

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

### Design consequence

The capability profile's influence on the `upsd` operand is **config rendering only**. A profile
should be able to change which driver runs, how it is parameterised, and how it is polled. It
should never change the pod's shape, replica count, resource allocation, or scheduling.

Stated as a boundary for the capability schema doc:

> Capability profiles render into NUT configuration. They do not render into Kubernetes pod
> specification.

### Open items this raises

**OD-21 · Driver configuration ownership.** Whether driver name, poll interval, and driver-specific
parameters move from `UPSDevice` spec into capability profiles, or remain in spec with profiles
supplying defaults and validation. Moving them removes per-device duplication; leaving them keeps
deployment-specific overrides simple. A hybrid — profile supplies the default, spec overrides — is
consistent with the precedence chain in RS-5 and is the likely answer.

## Scope, Provenance, and Non-NUT Power Devices

*Components: Capability Profiles.*

Which device classes get a profile at all, where a profile came from, and what happens to
power devices NUT cannot talk to.

### Profile scope by device class


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

### PDU profiles — parallel kind, scaffolding only

`powerman-pdu` is already in the driver allowlist (RB-2). A NUT-managed PDU with switchable outlets
is genuinely queryable and has device-specific instant-command behavior, so it will need capability
declarations once instant commands enter scope under OD-20.

**Decision: a parallel profile kind, not an extension of `UPSCapabilityProfile`.** Reusing the UPS
kind would keep the CRD count lower but make the name progressively misleading and force
UPS-specific fields to be optional-and-ignored for PDUs. A separate kind keeps each schema honest.

**Scope for v1: scaffolding only.** The kind exists with a minimal schema and a matcher path, and
no PDU actuation is implemented. The purpose is to establish the shape so that PDU support is an
additive change later rather than a refactor of the UPS profile schema.

Bundled catalog entries were originally excluded from that scope. They are included now: a schema
with no real device in it is a schema nobody has tested, and the first real profile is what turns
"the shape is established" from an assertion into something checkable. `spec.outlets` describes
layout only — declaring that an outlet switches is not a claim that this operator switches it.

Shared machinery that should be factored rather than duplicated when this is built: the deterministic
precedence chain (RS-5), semver rules (SB-9), the unidentified-device profile concept (PL-33), and the
verification lifecycle once OD-23 resolves it.

### Non-NUT power devices — RPS and the second actuation path

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

### Profile provenance

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
unidentified-device profile, and a growing `Community` set. The project cannot verify hardware it does not own,
and the provenance field is what makes that limitation legible rather than hidden.

`UserLocal` needs no maintainer action — it is simply what a user's own CRD profile is.

### Upgrade safety for user profiles

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

### FAQ entry

The user-facing answer to "Can I add my own capability profiles, and will an upgrade overwrite
them?" lives in `faq.md` and is not duplicated here. This document owns the reasoning behind it;
`faq.md` owns the wording users read.

### Open decisions

**OD-24 · Non-NUT power device actuation.** Whether a second actuation path is introduced for power
devices without NUT drivers, or whether they remain permanently topological. Deferred to be decided
alongside OD-10 (USB and serial UPS support), since both concern control surfaces outside the
NUT-network-only posture of RB-1 and SB-2a.

**OD-25 · PDU profile kind.** Schema shape for the parallel PDU capability kind, and which machinery
is factored out of `UPSCapabilityProfile` for shared use. Scaffolding only in v1.

**OD-26 · Provenance field semantics.** Whether `provenance` is advisory metadata or affects
resolution — for example, whether a `Community` profile should require explicit opt-in, or emit a
warning condition when matched.

## Testing Notes

For resolver-only testing, a user inventory object can reference a catalog profile by supplying a
model hint:

```yaml
apiVersion: power.zalud.io/v1alpha1
kind: UPSDevice
metadata:
  name: rack-a-ups
spec:
  identity:
    model: TOWER_1000VA_230V
  driver: dummy-ups
  powerDomains:
    - rack-a
```

For relay rendering, model the appliance as an upstream NUT server:

```yaml
apiVersion: power.zalud.io/v1alpha1
kind: UPSDevice
metadata:
  name: unifi-ups-tower
spec:
  identity:
    model: TOWER_1000VA_120V
  upstreamNUT:
    host: ups-tower.example.net
    port: 3493
    upsName: ups
    auth:
      mode: None
  powerDomains:
    - rack-a
```

That exercises profile matching, `dummy-ups` repeater rendering, NetworkPolicy egress rendering,
bounded TCP upstream reachability status, read-only NUT variable polling, and durable telemetry
audit writes.

## Field Verification (2026-08-04)

Verified against real `UPS 2U` and `UPS Tower` hardware running firmware `1.6.1`:

- TCP 3493 (NUT's default port) is closed/refused on every unit tested. The "devices expose a
  built-in NUT server rather than SNMP" quirk recorded above does not hold on this firmware —
  either it was never accurate, applied only to older/different hardware, or the feature exists
  but isn't enabled. This is exactly the scenario `F-26` (firmware-gated quirks can't expire)
  describes: the quirk has no firmware scoping and nothing flags it as needing re-verification.
  Needs a decision: correct the bundled quirk, or add firmware-scoping before trusting it further.
- SNMP is reachable and is the working telemetry path on this firmware, but only SNMPv3
  (`secLevel=authPriv`) is offered — no v1/v2c community-string option was present in the UI for
  these devices.
- Enable path (verified working, this app version): UniFi Network app → **Settings → CyberSecure →
  Traffic Logging → SNMP**. Per-version menu location is not guaranteed — if this doesn't match,
  use the in-app search for "SNMP" rather than guessing.
- `ups.conf` keys for SNMPv3 (verified against NUT's own docs, not guessed):

  ```ini
  snmp_version = v3
  secLevel = authPriv
  secName = <username>
  authPassword = <auth password>
  authProtocol = MD5|SHA|SHA256|SHA384|SHA512
  privPassword = <priv password>
  privProtocol = DES|AES|AES192|AES256
  ```

  Source: [snmp-ups(8) — networkupstools.org](https://networkupstools.org/docs/man/snmp-ups.html).
  These are driver credential fields, not profile-level data — they belong on
  `UPSDevice.spec.credentialSecretRef` (wired into the render path as of this date; see NUT
  Server / upsd in `docs/tasks.md`), not hardcoded into the bundled profile. A per-site SNMP
  community/credential is a cluster/site config value, not a fixed vendor default, so it doesn't
  belong in the profile catalog even when it happens to be the same across every unit on one site.

## Public Research Inputs

- Ubiquiti Store: UPS Tower and UPS 2U product pages list NUT compatibility for third-party devices.
- Ubiquiti Tech Specs: UPS Tower is the desktop `UPS-Tower-*` product; UPS 2U is the rack-mount
  `UPS-2U-*` product.
- Ubiquiti Community: UniFi UPS `1.4.18` release notes reported fixes for incorrect NUT protocol
  responses; later `1.5.0` notes mention stability improvements.
- Community `upsc` outputs reported `TOWER_1000VA_230V` and `2U_1500VA_230V` model strings and the
  telemetry variables used as the first-pass profile declarations.
