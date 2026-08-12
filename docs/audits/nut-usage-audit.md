# NUT Usage and Fidelity Audit

Status: audit record, 2026-08-03, against commit `00eb3c0`. Static reading only; no live NUT
session tested.

Components: NUT Server / upsd, Node Agent / DaemonSet, Telemetry & Triggers.

Scope: cross-component. Which NUT mechanisms the system uses, which it declines, and whether the
layering stays upstream-loyal with enhancements stacked on top rather than replacing NUT's own
machinery.

This audit spans three separate components, and the distinction matters:

| Component | Runs | Rendered by | Role |
| --- | --- | --- | --- |
| `NUTServer` | `upsd` | `nutserver_render.go` | Serves the NUT protocol on TCP 3493 |
| `NodePowerAgent` | `upsmon` + actuator | `nodepoweragent_render.go` | Client of `upsd`; host-level actuation |
| Operator | `internal/nut/client.go` | — | Also a client of `upsd`, for telemetry |

Per-component findings live in `nutserver-pod-audit.md` and `node-agent-daemonset-audit.md`.

Findings use the shared `F-n` namespace.

## What is used correctly

**Protocol fidelity.** `internal/nut/client.go` speaks the real protocol — `LIST VAR` with
`BEGIN LIST VAR` / `END LIST VAR` framing and `ERR` handling — rather than shelling out to `upsc`.

**Variable coverage is standard-conformant.** `ups.status`, `battery.charge`, `battery.runtime`,
`battery.voltage`, `battery.low`, `input.voltage`, `input.frequency`, `input.transfer.high`,
`input.transfer.low`, `output.*`, `ups.load`, `ups.temperature`, `ups.mfr`, `ups.model`,
`ups.serial`, `ups.firmware`, `ups.type`, `ups.test.*`. Real NUT names throughout, no invented
schema.

**`MODE=netclient`** is correct for the network-only posture of RB-1.

**Every agent connects as `secondary`.** This is the SB-3 authority boundary expressed in NUT's own
vocabulary rather than bolted alongside it — no agent believes it owns the shutdown decision.

**`SHUTDOWNCMD "/usr/local/bin/power-signal-writer"`** is the local handoff from NUT into the
project-owned actuator protocol. NUT still runs its complete state machine and invokes the shutdown
command; the command writes a structured, TTL-bound signal that remains dry-run unless the
`NodePowerAgent` is explicitly approved for actuation.

**`upsmon.conf` keyword set is correct**, and the timing keywords are exposed as CR fields rather
than hardcoded: `POLLFREQ` (15s), `POLLFREQALERT` (5s), `HOSTSYNC` (15s), `DEADTIME` (45s),
`FINALDELAY` (10s). `POWERDOWNFLAG` is derived from the agent. `MINSUPPLIES` and the `MONITOR` power
value are hardcoded — see `F-45`.

## Findings

**F-20 · `FSD` is referenced but the forced-shutdown path is not exercised.** FSD is NUT's mechanism
for a primary to declare a forced shutdown that all secondaries observe and act on. The current
architecture reaches the same outcome through the executor and signal file instead.

That is a legitimate choice — the operator needs ordering NUT cannot express — but it leaves the
NUT-native broadcast path sitting unused beside a parallel mechanism. Either document that FSD is
deliberately declined and why, or adopt it as the final release signal so shutdown is observable
through standard NUT tooling. Ambiguity here invites a future contributor to wire it up in parallel
with the executor.

**F-21 · `upssched` is not used at all.** NUT's own timer and event scheduler handles delayed
actions, escalation on sustained conditions, and per-event command dispatch. The operator
reimplements equivalent logic in Go.

Defensible: the operator needs cluster-wide correlation and dependency ordering that `upssched`
cannot provide, and centralizing decisions serves planner determinism. But it should be recorded as
a decision rather than left as an omission — "we use NUT fully" and "we decline NUT's scheduler"
need reconciling in the same document.

*Resolved 2026-08-03: `upssched` is deliberately declined, not an omission.* `upssched` fires
per-node, per-`upsmon`, off local NUT events only — it has no view of the cluster, other nodes, or
`ShutdownFlow` dependency state, so it structurally cannot produce a cluster-wide plan. The
planner's determinism guarantee (`PL-27`, `PL-28`: identical structural input produces a
byte-identical plan) also requires escalation logic to run through one deterministic, testable
package rather than N independent per-node `upssched` instances reacting to local timing. This
decision does not need its own `OD` entry — it follows directly from `SB-2b` (NUT's threshold model
is an input, never the sequencer) and `GP-4` (consume signals, do not rebuild them): `upssched` is a
sequencer, and sequencing is reserved for the operator.

**F-22 · Instant commands and writable variables are entirely unused.** No `upscmd`, `upsrw`, or
`INSTCMD` anywhere in the codebase. This is the whole SB-2c capability surface: `load.off`,
`load.on`, `shutdown.return`, `shutdown.stayoff`, `beeper.mute`, `test.battery.start`, and writable
settings including `ups.delay.shutdown` and `ups.delay.start`.

The consequence with the clearest operational cost: **no `shutdown.return` handshake.** The
canonical NUT ending is that the last secondary disconnects, the primary commands the UPS to cut
power after a delay, and the UPS restores power when line power returns. Without it, nodes power
off while the UPS continues draining battery into a load that is no longer doing anything.

Second: **no battery self-test invocation.** `ups.test.result` is already read, but
`test.battery.start` is not exposed. Battery health determines whether the runtime figures the
planner depends on are trustworthy at all.

Support for these varies sharply by device and driver, so they belong in the capability profile
actuation section per CR-2 — worth recording there even before any of them are implemented.

Noted and explicitly out of scope: `ups.delay.start` and the power-return cycle touch recovery,
which OD-1 closed. Recorded here only so the capability surface is complete, not as a reopening.

*Corrected 2026-08-03: this finding overstated the unused surface. The `dummy-ups` repeater/relay
path is implemented and upstream-loyal (`docs/design/upstream-nut-relay.md`). F-22 stands only for
instant commands and writable variables. See the correction in `quirks-aliasing-firmware.md`.*

**F-24 · Credential handling: correct validation, plaintext in a rendered Secret.** Config values
are validated against injection before being written, which is good. The rendered `upsmon.conf`
carries the monitor password in cleartext inside a Kubernetes Secret — conventional and unavoidable
given NUT's config format. Two things to confirm rather than assume: that the Secret is never
logged or echoed into events on render failure, and that the config hash in
`ManagedResourceStatus` cannot leak the value.

## Modularity assessment

The layering is sound. NUT is not forked, not patched, and not bypassed at the protocol level. The
operator generates standard config, speaks the standard protocol, and expresses its own authority
model inside NUT's vocabulary.

One architectural asymmetry is worth stating explicitly in the design docs: the **decision** layer
sits entirely above NUT — no FSD, no `upssched` — while the **observation** layer sits inside it.
That is the right split for this project, but the current documents describe NUT as an abstraction
and event source without saying which of NUT's own control mechanisms are deliberately declined.

## Recommended order

1. F-22 record the instant-command surface in capability profiles, ahead of implementation.
2. F-20 and F-21 write the FSD and `upssched` decisions down explicitly.
3. F-24 confirm no credential leakage through logs, events, or hashes.

## New open decisions proposed

**OD-19 · FSD usage.** Whether NUT's forced-shutdown broadcast becomes the final release signal or
is deliberately declined in favor of the executor's signal file. Affects whether shutdown is
observable through standard NUT tooling.

**OD-20 · Instant command scope and gating.** Which commands enter scope, how they are gated given
they can cut power to equipment, and which capability profile fields declare support. Bounded by
OD-1 on anything touching power-return.

## Findings — second pass, 2026-08-08

Re-read of the protocol-TLS path specifically, prompted by tracing what `spec.tls` reaches. Static
reading against NUT's `upsd.conf(5)`, `upsmon.conf(5)`, and `server/netssl.c`. Continues the `F-n`
namespace from `F-36`.

**F-37 · `spec.tls` mounted a certificate and never told NUT to use it.** The webhook defaulted
`spec.tls.mode` to `Required` and rejected `Required` without a `serverCertificateRef`; the render
mounted that Secret read-only at `/etc/nut/tls`. Nothing then emitted a single TLS directive:
`renderNUTServerConfig` wrote `upsd.conf` as a bare `LISTEN <address> <port>`, and the operand image
entrypoint adds nothing. `upsd` negotiates STARTTLS only when `CERTFILE` (OpenSSL builds) names a
usable key pair, so a `NUTServer` reporting TLS `Required` served **plaintext** NUT on 3493 —
including the `MONITOR <ups> 1 <user> <pass> secondary` login every `upsmon` sends on connect.
Confined to in-cluster traffic and constrained by the operand `NetworkPolicy`, but the API claimed a
protection that was not in effect: the same "declared field that does nothing" class as `F-25` and
`F-33`, and the only instance of it that was security-relevant.

Fixed on both sides of the connection. `upsd.conf` now renders `CERTFILE`, plus `DISABLE_WEAK_SSL`
(TLS 1.2 floor) and `CERTPATH`/`CERTREQUEST` when client certificate validation is enabled;
`upsmon.conf` renders `CERTPATH`, `CERTVERIFY`, and `FORCESSL`. Directive presence and the
Disabled-mode absence are both asserted in `internal/controller/nut_tls_render_test.go`.

Three constraints surfaced while fixing it, each of which shaped the result:

- **`CERTFILE` takes one file, a Kubernetes TLS Secret projects two.** NUT documents the file as the
  subject certificate, then intermediates, then the private key last. An init container running the
  same operand image concatenates `tls.crt` and `tls.key` into a memory-backed `emptyDir`, because
  the operand image is caller-supplied and the operator does not control its entrypoint.
  cert-manager's `CombinedPEM` output format was rejected as the mechanism: it writes key-first,
  which OpenSSL happens to tolerate and NSS builds do not use at all.
- **Client certificate validation is not deliverable today, so it stopped being a default.**
  `verifyClientCertificates` defaulted to `true` and forced `clientCARef` to be set. Rendering
  `CERTREQUEST 2` under that default would have locked out every `NodePowerAgent`, because the
  operator issues no client identity at all. Now defaults to `false`; the remaining work is tracked
  in `docs/tasks.md`. **The version reasoning originally recorded here was wrong — see `F-39`.**
- **`CERTVERIFY`/`FORCESSL` are process-global in `upsmon`.** An agent monitoring several
  `NUTServer`s cannot hold a different posture per server without `CERTHOST` (post-2.8.5 under
  OpenSSL — see `F-39`). Mixed
  modes therefore render the weakest common posture and set a `NUTTLSDowngraded` Degraded condition,
  rather than either cutting off the laxer server or silently under-securing the strict one.

A new field, `spec.tls.serverCARef`, carries the CA that clients verify `upsd` against. It is
distinct from `clientCARef`, which is the CA `upsd` verifies clients against — NUT keeps both under
the name `CERTPATH`, in different files, which is what made the original conflation easy.

## Findings — third pass, 2026-08-08

Prompted by a challenge to the version claims in `F-37`. Verified by running the operand image
rather than by reading documentation, which is what turned a citation error into a live finding.

**F-39 · The `F-37` fix is inert on the operand image, which is an NSS build.** `images/nut-server`
is `apk add nut` on `alpine:3.22`. That package is **NUT 2.8.2 linked against NSS**, not OpenSSL:

```console
$ docker run --rm alpine:3.22 sh -c 'apk add -q nut && upsd -V && ldd /usr/sbin/upsd | grep -Ei "ssl|nss"'
Network UPS Tools upsd 2.8.2
	libssl3.so => /usr/lib/libssl3.so
	libnss3.so => /usr/lib/libnss3.so
	libnspr4.so => /usr/lib/libnspr4.so
```

`upsd.conf(5)` documents `CERTFILE` as OpenSSL-only — "No-op in NSS builds" — and NSS builds
configure TLS through `CERTPATH` naming a three-file certificate database plus `CERTIDENT`. Feeding
the operator's own rendered configuration to that build:

```console
upsd.conf: invalid directive CERTFILE /etc/nut/tls/combined.pem
STARTTLS  → ERR FEATURE-NOT-CONFIGURED
LIST UPS  → BEGIN LIST UPS ... END LIST UPS
```

So `spec.tls.mode: Required` still serves plaintext NUT on 3493, `MONITOR` password included. `F-37`
is the same finding twice: the directive was correct for the backend the code assumed and wrong for
the backend actually shipped, and the API still claims a protection that is not in effect.

Why the existing tests missed it: `internal/controller/nut_tls_render_test.go` asserts the operator
*renders* `CERTFILE`, which it does. Nothing asserted that `upsd` *accepts* it, and the `kind` e2e
suite never performs a NUT TLS handshake. Rendering correctness and protocol correctness were never
connected, so the operand was free to reject every directive without a single test noticing.

The version claims recorded under `F-37` were also wrong and are corrected here. NUT 2.8.5 is the
current stable release (2026-04-07); 2.8.6 does not exist yet, and the "since 2.8.6" notes come from
documentation built off `master`. Those notes are real but they gate *unreleased* behavior, so
`F-37`'s statement that `CERTREQUEST` "is honored under OpenSSL from 2.8.6 onward" describes
something no released NUT can do. `CERTHOST` was recorded as "NUT 2.8.5+"; under OpenSSL it is also
post-2.8.5. Under NSS, `CERTREQUEST`, `CERTIDENT`, and `CERTHOST` have all worked for far longer —
the version gates apply to the OpenSSL backend only.

Remedy, and the reason it is a build decision rather than a render decision: the operand should stop
using the distribution package and build NUT 2.8.5 from source with `--with-openssl`. That makes
`CERTFILE` valid, keeps every credential a PEM file — the shape a Kubernetes TLS Secret already
projects — and avoids provisioning an NSS certificate database inside a container at all. Pinning
the source build also removes the silent-downgrade risk of a base-image bump changing the TLS
backend underneath the operator. Client certificates and per-server `CERTHOST` posture stay out of
v1 either way: on the OpenSSL path they need a release that does not exist, and on the NSS path they
need cert-database plumbing that the source build exists to avoid.

## Findings — fourth pass, 2026-08-09

Found by building the operands from source to resolve `F-39` and then running the two
of them against each other. Both findings are the same shape as `F-39`: a directive
the operator renders in a form NUT does not consume.

**F-40 · `CERTPATH` was rendered as a file, and OpenSSL reads it as a directory.**
`upsmon.conf(5)` states that `CERTPATH` accepts "a directory containing CA
certificates in PEM format, or alternatively, a single PEM file with multiple CA
certificates." The second half is not true of the OpenSSL client. `clients/upsclient.c`
calls:

```c
ret = SSL_CTX_load_verify_locations(ssl_ctx, NULL, certpath);
```

`certpath` is the third argument, `CApath`, and never the second, `CAfile`. OpenSSL
walks a `CApath` as a directory of certificates named by subject hash, so pointing it
at a PEM file loads without error — the call is lazy — and then finds no issuer for
anything. With `CERTVERIFY 1` and `FORCESSL 1`, both of which the operator renders for
`spec.tls.mode: Required`, that is not a silent weakening but a hard failure:

```text
upscli_sslinit: SSL_connect failed (SSL_ERROR 1)
Can not connect to NUT server nut-server in SSL, disconnect
UPS [smokeups@nut-server]: connect failed: SSL error
```

An agent in that state has no UPS telemetry at all, so a `NUTServer` at `Required`
would have taken every `NodePowerAgent` monitoring it offline. Fixed by mounting the
rendered bundle read-only, rehashing it into a memory-backed `emptyDir` with
`openssl rehash` in an init container, and pointing `CERTPATH` at that directory. The
projected Secret cannot carry the symlinks itself, which is why the copy exists.

The original code comment asserted the man page's wording as fact. Reading the source
would have settled it; reading the documentation did not.

**F-41 · `verifyClientCertificates` cannot work on any released NUT with OpenSSL.**
`server/netssl.c` in 2.8.5 ends its OpenSSL initialization with:

```c
SSL_CTX_set_verify(ssl_ctx, SSL_VERIFY_NONE, NULL);
```

There is no `SSL_CTX_load_verify_locations` call in that branch at all, so `upsd`
never loads a client CA and never requests a client certificate, whatever
`CERTREQUEST` and `CERTPATH` say. Those directives are honored under NSS, and under
OpenSSL only after 2.8.5 — which is unreleased. The field already defaults to `false`
after `F-37`, so nothing is currently mis-serving, but a user who sets it would get
mutual TLS in the API and none on the wire.

This is the `F-25`/`F-33`/`F-37` class again, and it is recorded rather than fixed
because the fix is a NUT release. The remedy when that release lands is in
`docs/tasks.md`; until then the field's documentation must say plainly that it is
inert, which is a smaller claim than the one the API currently makes.

**F-45 · `MINSUPPLIES` and the `MONITOR` power value are hardcoded, and this document
said otherwise.** The keyword list above claims the `upsmon.conf` set is "exposed as CR
fields rather than hardcoded" and names `MINSUPPLIES` among them. It is not.
`renderNodePowerAgentSecret` writes a literal `MINSUPPLIES 1`, and every `MONITOR` line
is emitted with a literal power value of `1`. `UpsmonConfigSpec` carries
`pollFrequency`, `alertPollFrequency`, `deadTime`, `hostSync`, and `finalDelay` — and no
supplies field at all.

The two literals are NUT's own redundancy model, which the project has therefore
adopted by accident rather than by decision. `MONITOR <system> <powervalue>` states how
many of this host's power supplies that UPS feeds, and `MINSUPPLIES <n>` states how many
supplies must be fed for the host to keep running. A dual-PSU host monitoring two UPS
devices at power value 1 with `MINSUPPLIES 1` stays up while either one is online, and
`upsc` still reports each device's state separately — an aggregate that is healthy while
any member is healthy, with per-member state individually visible.

That model is unreachable here for the case it exists to serve. A host whose two
supplies are fed by one UPS needs `MONITOR ups 2` and `MINSUPPLIES 2`; the hardcoded `1`
declares that host safe while it is losing power. The values are right for the
single-feed default and wrong for the topology the feature is for.

It also contradicts the operator's own aggregation. `powerObservationFromDevices`
reduces pessimistically — one device on battery makes the whole flow on-battery — while
`MINSUPPLIES 1` says a redundantly-fed node is unaffected until the last feed drops.
Both are defensible; disagreeing silently is not. Which one is correct depends on
whether two `feeds` edges into one node mean redundancy or coincidence, and the
inventory model does not currently say.

Fix: derive the power value and `MINSUPPLIES` from `feeds` edges rather than hardcoding
them, and correct the keyword claim above. Blocked on `OD-35`.
