# Reference

Components: Cross-cutting.
Audience: operators and integrators.

Look-up material. Nothing here is a walkthrough.

- [API reference](api.md) — every `power.zalud.io/v1alpha1` kind, what it is for, how the kinds relate,
  and the storage model.
- [Glossary](glossary.md) — every term this project uses, and the ones it deliberately refuses to use
  for ordering.
- [Metrics](metrics.md) — every published metric, its labels, and what is worth alerting on.
- [Security](security.md) — the privilege boundary, the webhook certificate reasoning, RBAC scope,
  network and credential controls, and supply-chain posture.
- [Image strategy](images.md) — why the operand images are built rather than pulled, and how to
  verify a digest.

Field-level schemas are not duplicated here. `kubectl explain <kind>` after install, or
`config/crd/bases/` in the repository, is generated from the Go types and is always current.

Also reference in practice: the [worked examples](../examples/README.md), whose manifests are
schema-validated in CI, and [Troubleshooting](../troubleshooting.md).
