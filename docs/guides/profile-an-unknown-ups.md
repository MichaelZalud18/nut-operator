# Profiling a UPS the catalog does not cover

Components: Capability Profiles.
Audience: operators.

**The decision:** what to do when your UPS matches no bundled capability profile — which, for most
hardware, is what will happen.

The project ships profiles only for devices it has verified, and UPS hardware varies enormously in
what it reports over NUT. A device the catalog does not cover still works: telemetry is polled, the
plan compiles, and dry-run behaves normally. What you lose is the operator knowing anything about the
device, and that has one concrete consequence.

## What an unidentified device costs you

A device matching no product profile falls through to the **unidentified-device profile**, the
guaranteed-match terminal entry in the matching chain. Matching it means nothing is known about the
device — not that the device has a known minimum capability.

The flow raises `UnidentifiedUPSDevice` and **refuses to enforce**. Dry-run still compiles and
publishes the whole plan, so you lose nothing while evaluating. Enforcement stays blocked until
either a profile matches, or `spec.safety.allowUnidentifiedDevices: true` records that you accepted
the gap — in Git, where someone can see it.

Profiling the device is the option that removes the refusal rather than overriding it.

## 1. Draft a profile from what the device reports

Point a `UPSCapabilityProbe` at your `UPSDevice`:

```yaml
apiVersion: power.zalud.io/v1alpha1
kind: UPSCapabilityProbe
metadata:
  name: unidentified-rack-a-ups
spec:
  deviceRef:
    name: rack-a-ups
```

The operator reads what the device actually reports and writes a ready-to-apply profile into
`status.draftProfile`, along with the variables it saw, any non-standard names worth a look, and
suggested aliases where a standard reading appears to have arrived under a different name:

```sh
kubectl get upscapabilityprobe unidentified-rack-a-ups \
  -o jsonpath='{.status.draftProfile}' > my-ups-profile.yaml
```

The probe is advisory by construction: it never changes how a device resolves, and it never runs on
the failure path. Nothing you do here can affect a shutdown in progress.

## 2. Review it before applying

**Read the draft.** It declares only what the device demonstrably reported, and anything inferred
rather than observed is marked as a suggestion in a comment.

The actuation section is deliberately left empty. Actuation commands can cut power to equipment, so
support for them is declared only after someone has verified them against the firmware actually
running on the device. An empty actuation section is the honest starting state, not an omission to
fill in from a datasheet.

Apply it like any other resource. A CRD-authored profile always outranks a bundled one, so your
profile wins over anything the catalog ships for the same match.

## 3. Send it upstream

`status.issueReport` is the same findings formatted for a GitHub issue, with a verification
checklist:

```sh
kubectl get upscapabilityprobe unidentified-rack-a-ups \
  -o jsonpath='{.status.issueReport}'
```

Open an issue with that and the profile can go into the bundled catalog, so the next person with your
hardware does not repeat the work. This is how the catalog grows — it has no other source.

## Related

- [Preparing the hardware](prepare-your-hardware.md) — getting the device reachable in the first
  place, which has to work before a probe can read anything.
- [API reference](../reference/api.md#capability-catalog) — `UPSCapabilityProfile` and
  `UPSCapabilityProbe`.
- [The capability profile design](../contributing/design/capability-profiles.md) — matching rules,
  versioning, and why declaration is authoritative while probing stays advisory.
