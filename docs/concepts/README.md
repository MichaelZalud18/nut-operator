# Concepts

Components: Cross-cutting.

What the system is and how its parts relate. Read these when you want to understand the shape of the
thing; read [Guides](../guides/README.md) when you want to decide something, and
[Reference](../reference/README.md) when you want to look something up.

- [Architecture](architecture.md) — the control plane, the two operands, and durable state; how a
  power event moves through them; and the full component map.
- [Pod placement](pod-placement.md) — where each pod lands and what pins it there, worked against the
  `orion-cluster` example.

The vocabulary these pages use — **power domain**, **feeds** and **carries**, **tier** versus
**wave** — is defined once in the [glossary](../reference/glossary.md). Tiers are what you author;
waves are what the planner derives.
