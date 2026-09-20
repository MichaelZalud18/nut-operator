# Upstream NUT Relay

Components: NUT Server / upsd.
Audience: contributors.

Some UPS appliances expose their own NUT `upsd` endpoint. `nut-operator` models those as
`UPSDevice.spec.upstreamNUT` and renders them through NUT `dummy-ups` repeater mode.

Official NUT behavior used by this design:

- `dummy-ups` repeater mode forwards data from a locally or remotely served NUT UPS.
- The repeated target is rendered as `<upsname>@<hostname>[:<port>]`.
- The pinned operand has no `authconf` option. Only `auth.mode: None` is accepted.
- Strict startup is the NUT default; `repeater_disable_strict_start` makes startup tolerate an
  upstream that is unavailable at process start.

Reference: [pinned NUT 2.8.5 driver source](https://github.com/networkupstools/nut/blob/v2.8.5/drivers/dummy-ups.c).

Compatibility evidence: the current online manual's `authconf` support is newer than the
repository's pinned NUT 2.8.5. The [MOD-3 real-binary investigation](../audits/mod-3-external-planning-2026-09-18.md#live-relay-experiment-and-compatibility-finding)
reproduces the old generated-config startup failure. Rendering now omits that unsupported
option, and both admission and renderer validation reject `Default`/`Secret` auth modes.
None performs no upstream authentication or certificate verification; NUT may attempt
opportunistic TLS, which is not verified-TLS policy. Do not use this relay when upstream
credentials or verified TLS are required. Downstream NUTServer TLS remains independent.

## API Shape

```yaml
apiVersion: power.zalud.io/v1alpha1
kind: UPSDevice
metadata:
  name: unifi-ups-2u
spec:
  identity:
    model: 2U_1440VA_120V
  upstreamNUT:
    host: ups-2u.example.net
    port: 3493
    upsName: ups
    auth:
      mode: None
    strictStart: true
  powerDomains:
    - rack-a
```

`spec.driver` is optional when `spec.upstreamNUT` is set. If present, it must be `dummy-ups`.
`spec.endpoint` and `spec.credentialSecretRef` are intentionally rejected with `upstreamNUT` so
the relay path cannot conflict with direct network-driver configuration.

## Rendering

The generated `ups.conf` entry uses:

```conf
[unifi-ups-2u]
  driver = dummy-ups
  port = ups@ups-2u.example.net:3493
  mode = repeater
```

`Default` and `Secret` remain reserved API values but are explicitly rejected with the shipped
operand. Supplying another image does not bypass that validation. Support requires a compatible
renderer, admission contract and real-binary acceptance; it is not silently enabled by a tag.

## Status

`NUTServer.status.upstreamNUT` reports each selected upstream device, the local NUT name, upstream
host/port, auth mode, strict-start mode, and the most recent bounded TCP reachability probe. This
is a transport probe only. In a full install, read-only NUT variable polling and durable telemetry
writes use the normal telemetry path. In the NUT-only profile, external consumers query the NUT
protocol directly; there is no UPSDevice status polling or database path.

## Network Policy

When a selected device uses `upstreamNUT`, the generated NUTServer NetworkPolicy adds egress for:

- TCP to the selected upstream NUT ports.
- TCP/UDP DNS on port 53.

Kubernetes NetworkPolicy has no portable FQDN selector, so destination scoping remains a packaging
or CNI-specific hardening layer.
