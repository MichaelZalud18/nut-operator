# Upstream NUT Relay

Components: NUT Server / upsd.
Audience: contributors.

Some UPS appliances expose their own NUT `upsd` endpoint. `nut-operator` models those as
`UPSDevice.spec.upstreamNUT` and renders them through NUT `dummy-ups` repeater mode.

Official NUT behavior used by this design:

- `dummy-ups` repeater mode forwards data from a locally or remotely served NUT UPS.
- The repeated target is rendered as `<upsname>@<hostname>[:<port>]`.
- `authconf` may be `none`, `default`, or an explicit file path.
- Strict startup is the NUT default; `repeater_disable_strict_start` makes startup tolerate an
  upstream that is unavailable at process start.

Reference: <https://networkupstools.org/docs/man/dummy-ups.html>

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
  authconf = none
```

When `auth.mode: Secret`, the Secret key must contain a complete `nutauth.conf` file and must be
in the rendered NUTServer operand namespace. The operator projects it under
`/etc/nut/upstream-auth/<local-nut-name>.nutauth.conf` and renders that path as `authconf`.

## Status

`NUTServer.status.upstreamNUT` reports each selected upstream device, the local NUT name, upstream
host/port, auth mode, strict-start mode, and the most recent bounded TCP reachability probe. This
is a transport probe only; read-only NUT variable polling and durable telemetry writes run through
the normal telemetry path.

## Network Policy

When a selected device uses `upstreamNUT`, the generated NUTServer NetworkPolicy adds egress for:

- TCP to the selected upstream NUT ports.
- TCP/UDP DNS on port 53.

Kubernetes NetworkPolicy has no portable FQDN selector, so destination scoping remains a packaging
or CNI-specific hardening layer.
