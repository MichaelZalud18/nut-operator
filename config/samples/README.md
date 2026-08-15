# Sample manifests

One example of every CRD this operator serves, applied as a set:

```sh
kubectl apply -k config/samples/
```

They are a **reference for field shapes**, not a deployable cluster configuration. For a worked
example that hangs together as an architecture — one power domain, derived topology, and a flow that
runs against it — read [docs/examples/orion-cluster/](../../docs/examples/orion-cluster/).

## What is safe about them

Every sample is inert as written:

- `ShutdownFlow` and `NodePowerAgent` are `mode: DryRun`.
- `NodePowerAgent.spec.shutdown.actuatorPolicy` is `Simulate`, so the actuator records signals and
  touches no host.
- The flow sets `safety.requireManualApproval: true`, so even in `Enforce` it waits for the approval
  annotation.

Nothing here can halt a machine without deliberate edits to all three.

## What they reference but do not ship

Applying the set produces objects that reference things this directory cannot contain:

| Reference | Owned by |
| --- | --- |
| `rack-a-ups-snmp` Secret | Whoever holds the SNMP credentials |
| `rack-a-nut-server-tls`, `rack-a-nut-server-ca` Secrets | Your certificate tooling |
| `power-audit` CNPG `Cluster` | The CloudNativePG operator |
| Kubernetes `Node` objects and their labels | Whatever provisions your nodes |

Every reference *between* samples resolves within the set.

## Tiers and labels

`power_v1alpha1_powermanagementcluster.yaml` sets
`shutdownTiers.labelKey: power.zalud.io/shutdown-tier`, which is also the built-in default. That key
does double duty in these samples and it is worth knowing which is which:

- As a **selector**, in `namespaceSelector.matchLabels` — "act on namespaces tagged tier 4".
- As a **tier source**, for any group that omits `shutdownTier` — the operator reads the numeric
  value out of the group's own selector.

Every group in the samples states `shutdownTier` explicitly, so only the first use is exercised. A
group that dropped its explicit tier would fall back to the second, and would derive the same number
— which is the arrangement working as intended rather than a coincidence.

Ordering is always the number. Named tags like `application` or `data` are membership and never
order (`OD-4`).

## PDU profiles are scaffolding

`power_v1alpha1_pducapabilityprofile.yaml` validates, matches, and is consumed by nothing. `OD-25`
scopes PDU support for v1 to the kind, its schema, validation, the bundled catalog, and matcher
support — with no device kind, inventory entity, render path, or actuation path behind it.

## Keeping them correct

The CRDs are generated from the Go types; these manifests are hand-written. `make validate-samples`
checks every file here and under `docs/examples/` against the generated schemas, and runs in CI on
every commit with no path filter. It exists because a sample shipped a field shape the API server
would have rejected, and nothing caught it.
