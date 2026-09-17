# Importing topology from NetBox

Components: Inventory System.
Audience: operators and integrators.

Use this when NetBox already records your devices and cabling. The importer reads NetBox's REST API
and renders the same CRs the hand-authored path uses. It does not write to NetBox, and it does not
put a NetBox dependency in the shutdown path.

```sh
NETBOX_TOKEN=... go run ./cmd/netbox-inventory-sync \
  -url https://netbox.example.com \
  -tag power-managed \
  > power-inventory.yaml
```

Apply or commit the rendered YAML through the same GitOps path as hand-written inventory. The
default token scheme is `Bearer`, matching current NetBox v2 tokens. Use `-token-scheme Token` only
for legacy v1 tokens.

## NetBox fields

The importer uses native NetBox DCIM objects for relationships:

- `dcim.Device` supplies the device identity and role.
- `dcim.PowerPort` plus its connected endpoint renders `Feeds` edges. The downstream power-port
  name becomes `spec.input`.
- `dcim.Interface` plus its connected endpoint renders direct `Carries` edges when one side imports
  as `PowerInfrastructure` and the other side imports as a UPS or Kubernetes node.

Planner-only metadata lives in one NetBox custom field named `nut_operator` by default. Use
`-operator-field` if your NetBox instance uses a different JSON custom field name.

UPS example:

```json
{
  "kind": "UPSDevice",
  "name": "rack-a-ups",
  "powerDomains": ["rack-a"],
  "nut": {
    "driver": "snmp-ups",
    "endpointHost": "ups-a.example.com",
    "endpointPort": 161,
    "model": "TOWER_1000VA_230V"
  }
}
```

Node example:

```json
{
  "kind": "Node",
  "nodeName": "worker-a",
  "roles": {
    "shutdownTier": 2
  }
}
```

Infrastructure devices can be inferred from NetBox roles containing `pdu`, `switch`, `router`,
`power-panel`, or `network`. If the role is not enough, set the field explicitly:

```json
{
  "kind": "PowerInfrastructure",
  "name": "rack-a-switch",
  "infrastructureClass": "Switch"
}
```

## Validation

The command compiles the rendered provider-neutral snapshot before writing YAML. Structural errors
such as missing feed inputs, duplicate entities, or orphaned nodes fail the command instead of
emitting a manifest the planner would reject later. Connected power endpoints outside the imported
device set also fail, even if another feed would keep a node non-orphaned: include the required
upstream device in the selected inventory. Warnings are printed to stderr; credentials are
never rendered into the output.

For debugging the provider contract directly:

```sh
NETBOX_TOKEN=... go run ./cmd/netbox-inventory-sync \
  -url https://netbox.example.com \
  -tag power-managed \
  -format snapshot-json
```

## Compatibility testing

The [disposable NetBox integration suite](../../test/netbox/README.md) runs this command against
an explicitly pinned real NetBox service with synthetic DCIM inventory. It covers authentication,
pagination, tag selection, cabling, deterministic output and fail-closed inventory compilation.
Run `make test-netbox` on an authorized development Docker host; `make test-netbox-harness`
checks ownership and cleanup without Docker. The suite does not connect to an existing NetBox
instance or add NetBox to the operator's shutdown runtime.
