# Post-v1 Backlog

Work that is real and intended, but deliberately not part of v1.

This file exists so [tasks.md](tasks.md) can answer exactly one question — what is left before v1 —
without carrying items that cannot close no matter how much work is done. Everything here is
tracked, not dropped: a decision recorded in `docs/design/scope-boundaries.md` or an upstream
dependency outside this project is what puts an item on this page.

The bar for moving something here is narrow. "Hard", "unscheduled", or "nobody has picked it up"
are not reasons — those stay in `tasks.md` as open v1 work. The two qualifying reasons are:

- **Scope**, where `scope-boundaries.md` places the work beyond v1. Reversing that means reopening
  the decision, not quietly re-planning.
- **Upstream**, where the capability does not exist yet in software this project consumes. No amount
  of work here closes it.

Last reviewed: 2026-08-09

---

## Capability Profiles

### Non-NUT power device actuation (`OD-24`)

Power devices with no NUT driver — UniFi RPS-class hardware and similar — are modeled
topologically today: they appear in inventory and shape shutdown ordering, but the operator never
actuates them. Making them actuatable means a second actuation path alongside NUT.

**Why it is post-v1:** `SB-2a` makes NUT the power-state path unconditionally, and a second
actuation surface is precisely the exception that rule exists to prevent. `OD-24` is recorded as v2
scoping and is explicitly decided alongside `OD-10` (USB and serial support), because both are
control surfaces outside the NUT-network-only posture that the security narrative in
`docs/architecture.md` and `docs/images.md` rests on.

Reversing this is a scope decision, not a planning one.

---

## NUT Server / upsd

Both items below share one cause: mutual TLS between `upsmon` and `upsd` does not work on any
released NUT built against OpenSSL. `server/netssl.c` in 2.8.5 ends its OpenSSL initialization with
`SSL_CTX_set_verify(ssl_ctx, SSL_VERIFY_NONE, NULL)` and never loads a client CA, so the server
never requests a client certificate whatever the configuration says (`F-41`). The directives exist
and are documented; the implementation behind them landed after 2.8.5 and 2.8.6 is unreleased.

Server authentication and verified client connections both work today — see `F-39`/`F-40`. What is
missing is only the client proving its own identity.

### Client certificates for `upsmon`

The operator provisions no client identity, so `verifyClientCertificates` would lock out every
agent and correctly defaults to `false`. On the OpenSSL path this needs `CERTFILE` in `upsmon.conf`
and a working `CERTREQUEST`. On the NSS path it needs a certificate database, which the source
build exists to avoid (`OD-32`).

When it becomes buildable, the identity should be minted by the operator's own CA into a per-agent
Secret, matching `hack/webhook-cert.sh`. The cert-manager CSI driver is the wider ecosystem's answer
for per-pod identity and is rejected here for the same reason cert-manager itself was: it has
nothing to reconcile while the cluster is losing power.

### Per-server TLS posture via `CERTHOST`

`CERTVERIFY` and `FORCESSL` are process-global in `upsmon`, so an agent monitoring servers with
different `spec.tls.mode` values renders the weakest common posture and reports `NUTTLSDowngraded`.
`CERTHOST` would give each `MONITOR` line its own flags. Under OpenSSL it also lands after 2.8.5.

The v1 behavior is not silent: the downgrade sets a Degraded condition naming why, which is the
part that mattered.

---

## Moving an item back

An item returns to `tasks.md` when its gate is gone — a NUT release ships, or a scope decision is
formally reopened and re-recorded. Move the text back with the reason it was here removed, so the
tracker never carries a stale "post-v1" label for work that is now simply open.
