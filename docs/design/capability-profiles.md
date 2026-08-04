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
exact model and firmware, exact model, model glob, driver family, universal floor.

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

- The devices expose a built-in NUT server rather than SNMP.
- Firmware before `1.4.18` had reported NUT protocol response bugs.
- Credentialed reads may require normal NUT client configuration rather than ad-hoc `upsc` flags.
- The devices report `battery.low`; this is recorded as a quirk because the normal NUT low-charge
  name is `battery.charge.low`.
- Tower output power/current telemetry may vary by firmware or load until tested against real units.

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

## Public Research Inputs

- Ubiquiti Store: UPS Tower and UPS 2U product pages list NUT compatibility for third-party devices.
- Ubiquiti Tech Specs: UPS Tower is the desktop `UPS-Tower-*` product; UPS 2U is the rack-mount
  `UPS-2U-*` product.
- Ubiquiti Community: UniFi UPS `1.4.18` release notes reported fixes for incorrect NUT protocol
  responses; later `1.5.0` notes mention stability improvements.
- Community `upsc` outputs reported `TOWER_1000VA_230V` and `2U_1500VA_230V` model strings and the
  telemetry variables used as the first-pass profile declarations.
