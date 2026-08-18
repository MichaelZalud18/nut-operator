# Reconciler Watch Scoping

Status: findings record, 2026-08-09. `F-42` confirmed against a 10h production log pull and since
fixed; `F-43` from reading `SetupWithManager`, since fixed.

Components: Planning & Execution Logic, NUT Server / upsd.
Audience: contributors.

Two controllers get the same watch wrong in opposite directions. `ShutdownFlow` watches `UPSDevice`
with no predicate and reconciles on every telemetry tick; `NUTServer` does not watch it at all and
misses changes it is supposed to act on. They are recorded together because the fix is one shape —
a predicate scoped to the fields the controller actually reads — applied twice.

Findings continue the shared `F-n` namespace from F-41.

## Findings

**F-42 · `ShutdownFlow`'s unpredicated `UPSDevice` watch causes reconcile churn.** `SetupWithManager`
watches `UPSDevice` with no predicate, so every telemetry tick — 5–15s per device — re-enqueues a
`ShutdownFlow` reconcile. Each one performs a Postgres audit-store round trip via
`recordShutdownFlowAudit` regardless of whether anything the trigger logic reads actually changed.

Confirmed on real traffic: a 10h production log pull on 2026-08-04 showed 1,516 `"the object has been
modified"` errors spread evenly across the window (~1 every 48s), 744 of them against `ShutdownFlow`
specifically. Evidence trail is in the private deployment repository.

`F-31` fixed the symptom — status writes are patches, so the conflict errors stopped — but the churn
underneath is unchanged. The work is real and unnecessary rather than merely noisy: audit rows are
written for reconciles that observed nothing new.

Fix: scope the `UPSDevice` watch predicate to the fields the trigger logic reads — phase, charge
percentage, runtime seconds.

**F-43 · `NUTServer` watches neither `UPSDevice` nor unowned credential `Secret`s.**
`nutserver_controller.go`'s `SetupWithManager` watches `NUTServer` itself plus resources it owns.
`Owns(&corev1.Secret{})` matches only Secrets carrying an owner reference back to the `NUTServer` —
a user-supplied `credentialSecretRef` target has none, so it is not covered.

Consequence: a `UPSDevice.spec.credentialSecretRef` change, a `driverOptions` change, or a change to
the contents of the referenced Secret all silently do nothing until some unrelated reconcile happens
to fire. The operand keeps serving the previous configuration with nothing reporting drift.

This is what makes credential rotation an operator concern despite rotation itself being an
operations action. The operator's duty is not to rotate anything — it is to notice the referenced
Secret changed and re-render both sides in an order that does not lock agents out mid-outage. That
duty is currently unmet.

Fix: the predicate-scoped watch from `F-42`, plus a mapping watch from credential Secrets back to the
`NUTServer`s that reference them.

## Not findings

- `Owns(&corev1.Secret{})` is correct for the Secrets the controller renders. The gap is only for
  Secrets it reads but does not own.
- Reconciling on genuine telemetry-driven trigger changes is the intended behavior. `F-42` is about
  reconciling when nothing the trigger logic reads has moved.
