# Concepts

Components: Cross-cutting.

What the system is and how its parts relate. Read these when you want to understand the shape of the
thing; read [Guides](../guides/README.md) when you want to decide something.

- [Architecture](architecture.md) — the control plane, the two operands, and durable state, with the
  data flow between them.
- [System architecture](system-architecture.md) — the same shape at service level: what is the
  interface, what is compiled, what is published, and what a consumer subscribes to.
- [Pod placement](pod-placement.md) — where each pod lands and what pins it there, worked against the
  `orion-cluster` example.

The vocabulary these pages use — **power domain**, **feeds** and **carries**, **tier** versus
**wave** — is defined once in the [glossary](../reference/glossary.md).
