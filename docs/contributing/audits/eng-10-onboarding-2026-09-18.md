# ENG-10: Quickstart and Wizard Assessment

Components: Foundation & Documentation.
Audience: contributors.

## Decision

Use a strong standalone quickstart for v1. Defer wizard implementation to post-v1 under the
existing `ENG-10` ID. Track remaining first-user quickstart validation as `REL-6`; preserve
completed `REL-5` implementation and live qualification. Scope is recorded in
[SB-14](../design/scope-boundaries.md#sb-14--no-embedded-ui-in-v1); task status belongs in the
[post-v1](../../tasks-post-v1.md#foundation--documentation) and
[release](../../tasks-v1-release.md#qualification) trackers.

## Evidence and Tradeoffs

The [two-UPS quickstart](../../examples/quickstart/README.md) already provides three-domain
manifests, a topology renderer accepting three explicit node names, expected waves, safe defaults,
and cleanup. The [configuration guide](../../installation/configuration.md) links the production
credential, TLS, storage, and topology contracts. Completed REL-5 and TEST-3 record component and
live install-to-plan evidence. These assets provide a usable foundation without a new interface.

A wizard could reduce repetitive YAML editing, collect related inputs in order, and catch missing
bindings earlier. It cannot know physical wiring, workload priorities, or acceptable shutdown
policy on the user's behalf. Supporting it also requires interaction design, packaging,
credential handling, cancellation behavior, and keeping generated resources aligned with APIs
and examples. With modular deployment profiles still under investigation, its supported paths
would also need explicit selection. These costs favor deferral, not abandonment.

The [Diataxis tutorial guidance](https://diataxis.fr/tutorials/) recommends a concrete successful
path, visible expected results, limited alternatives, and observation of users following the
instructions. Applied here, that supports validating the existing first-run experience before
adding an interactive interface. It does not establish that this project's quickstart is already
usable by a newcomer.

## Remaining Validation

This assessment reviewed documentation, the renderer, existing completion evidence, and the
quickstart E2E fixture. It did not run a new cluster or observe a first-time user.

- The quickstart assumes a disposable three-node cluster; validate a linked, reproducible path
  to that prerequisite, including tools, image access, and explicit cluster context.
- The E2E fixture uses server-side installer apply, substitutes test image references, discovers
  node roles, and waits for admission readiness. The documented path uses reader-selected nodes
  and public image references. A passing fixture does not prove those manual steps work verbatim.
- Validate that readiness and plan-review instructions make success observable, and that a reader
  understands the difference between compilation, healthy operands, and authorization to act.
- Observe installation through cleanup using only the public instructions; record and fix
  friction before calling onboarding validated. Preserve focused automated regression coverage.

REL-6 owns this work. Its findings should shape the post-v1 wizard's interface and acceptance
tests rather than requiring another adopt/reject investigation under ENG-10.
