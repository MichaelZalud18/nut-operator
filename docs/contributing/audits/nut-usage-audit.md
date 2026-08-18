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
path is implemented and upstream-loyal (`docs/contributing/design/upstream-nut-relay.md`). F-22 stands only for
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
(TLS 1.2 minimum) and `CERTPATH`/`CERTREQUEST` when client certificate validation is enabled;
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
said otherwise.** The keyword list above claimed the `upsmon.conf` set is "exposed as CR
fields rather than hardcoded" and named `MINSUPPLIES` among them. It is not.
`renderNodePowerAgentSecret` writes a literal `MINSUPPLIES 1`, and every `MONITOR` line
is emitted with a literal power value of `1`. `UpsmonConfigSpec` carries
`pollFrequency`, `alertPollFrequency`, `deadTime`, `hostSync`, and `finalDelay` — and no
supplies field at all. The sentence above is corrected; this records why it was wrong.

The literals are correct for the topology the project actually targets. `MONITOR
<system> <powervalue>` states how many of this host's power supplies that UPS feeds, and
`MINSUPPLIES <n>` how many must be fed for the host to keep running, so power value 1
with `MINSUPPLIES 1` means a host monitoring several UPS devices stays up while any one
of them is online. That is the same "healthy while any member is healthy, per-member
state still individually visible" shape the `upsd` readiness probe already implements in
`upsdReadinessProbeScript` (`F-17`), which exits 0 on the first device that answers
`upsc`. The two agree.

A host whose two supplies are fed by a single UPS would need `MONITOR ups 2` and
`MINSUPPLIES 2`, and cannot express that today. That topology is not currently modeled
anywhere in the inventory, so this is recorded as a limit of the render rather than as a
defect with a fix waiting on it.

## Findings — fifth pass, 2026-08-12

Two items about which NUT drivers the operand actually carries. Both were resolved by listing the
drivers in the shipped image rather than by reading the `configure` invocation, which is what turned
one of them from a suspicion into a fact.

*Verified — `ls /usr/lib/nut` in `example.com/nut-server:v0.0.1`, the source-built operand image
(NUT 2.8.5, OpenSSL, per `F-39`'s remedy):*

```text
adelsystem_cbi  apc_modbus  apcupsd-ups  clone  clone-outlet  dummy-ups  failover
generic_modbus  huawei-ups2000  must_ep2000pro  netxml-ups  phoenixcontact_modbus
skel  snmp-ups  socomec_jbus
```

The only Dockerfile change between that image and `main` is the `HEALTHCHECK` instruction
(`ffeca4a`), so its `configure` flags and its driver set are the ones this repository currently
ships.

**F-50 · The driver allowlist admits a driver the image does not contain.**
`supportedNetworkUPSDrivers` (`internal/webhook/v1alpha1/upsdevice_webhook.go:207-209`) returns
`dummy-ups`, `snmp-ups`, `netxml-ups`, `powerman-pdu`, and `apcupsd-ups`. Four of those five appear
in the listing above. **`powerman-pdu` does not.**

The cause is in the image: `images/nut-server/Dockerfile:64` passes `--without-powerman`. So a
`UPSDevice` naming `powerman-pdu` passes admission, renders into `ups.conf`, and can never start —
`upsdrvctl` has no such binary to exec. That is the `F-25`/`F-33`/`F-37` class again: an API that
accepts a configuration the implementation cannot honor. It differs from those three in being
resolvable in either direction, since building the driver in is a one-flag change.

`docs/images.md:26` repeats the allowlist including `powerman-pdu`, so the claim exists in two
places and both need correcting together — see `F-52` in
[operator-maturity-benchmarks.md](operator-maturity-benchmarks.md).

Whichever direction is chosen, the fix has to include an assertion of the allowlist against the
image in the smoke test. The two drifted apart silently once and nothing would stop it happening
again.

*Resolved 2026-08-12 by dropping the allowlist entry, not by building the driver.* Alpine does not
package `libpowerman` (`apk search libpowerman` returns nothing on 3.22), so building `powerman-pdu`
in means compiling a second dependency from source inside every operand image. That is the wrong
trade for a driver no device in the inventory uses, against an allowlist entry that could never
start.

The guard is deliberately two-sided, because neither side can see the other. `hack/smoke-image.sh`
asserts every allowlisted driver exists at `/usr/lib/nut/<driver>` in the image — that half can see
the image but not the Go allowlist. `TestSmokeTestCoversEveryAllowlistedDriver` parses the script's
`ALLOWLISTED_DRIVERS` line and compares it to `supportedNetworkUPSDrivers()` — that half can see the
allowlist but not the image. Pinned to each other they close the gap: a driver added to admission
without the smoke test fails in `go test`, and a driver in both lists that the image lacks fails in
the smoke test.

`TestPowermanPDUIsNotAdmitted` is separate on purpose. The comparison above passes just as happily
if someone re-adds the driver to both lists, which is exactly how it would come back.

Sabotage-verified in both directions: re-adding `powerman-pdu` to the Go allowlist failed both Go
tests; adding it to the smoke list alone failed the smoke test with
`admission allowlists powerman-pdu but the image does not contain it`.

The claim also existed in `README.md` and `docs/images.md:26`, and in the `UPSDevice.spec.driver`
doc comment that generates the CRD description. All four are corrected.

**OD-36 · `clone`, `clone-outlet`, and `failover` are built, unused, and undeclared.** All three
appear in the listing above — they are part of `NUTSW_DRIVERLIST` and are built unconditionally,
with no `configure` flag in `images/nut-server/Dockerfile` naming them either way. A search of the
repository for all three returns nothing outside `docs/tasks.md`: no allowlist entry, no
documentation, no decision.

Two of them bear directly on this project's own subject matter, which is why silence is the wrong
answer:

- **`clone`** is upstream's staged-shutdown mechanism — a virtual UPS presenting earlier thresholds
  than the device it shadows, so one physical UPS can drive several shutdown stages. That is the
  closest thing upstream has to the sequencer, and it sits in the image unmentioned.
- **`failover`** addresses the multi-supply-per-host topology `F-45` records as inexpressible today.

This is the same shape as `F-20` (FSD) and `F-21` (`upssched`): a NUT-native mechanism that the
project declines by omission rather than by decision. `F-21` was resolved by writing the reasoning
down, and that is the model. Either decline all three with reasons, alongside FSD and `upssched`, or
scope them.

The concrete risk of leaving them unnamed is precise: a contributor who finds `clone` in the image
and reads that this operator sequences staged shutdowns has every reason to wire one up in parallel
with the executor, which is exactly the split-brain `SB-2b` forbids.

*Resolved 2026-08-12: all three declined.* Recorded in
[scope-boundaries.md](../design/scope-boundaries.md) and the decision index, alongside FSD (`F-20`)
and `upssched` (`F-21`).

`clone` and `clone-outlet` fall to `SB-2b` directly — they are sequencers, and sequencing is the
operator's. The reasoning is `F-21`'s, and the closeness of the analogy is what makes writing it
down worth the effort: `upssched` is obviously a scheduler and obviously not wanted, while `clone`
looks like it would work, because a virtual UPS with earlier thresholds is a genuinely reasonable
way to stage a shutdown when there is no cluster to coordinate.

`failover` is declined for a different reason and should not be lumped in. It is not a sequencer;
it presents several physical devices as one, which really does resemble the topology `F-45` records
as inexpressible. It is declined because that gap is in the render — `MONITOR` power value and
`MINSUPPLIES` hardcoded to 1 — and in the inventory model, so adopting the driver would not close
it. Revisit only if `F-45` is built.

Nothing changes in the image: all three build unconditionally as part of `NUTSW_DRIVERLIST` and
there is no `configure` flag to exclude them, so the decision governs the admission allowlist and
the documentation. Enforcement is already in place as a side effect of `F-50` — the allowlist
rejects any driver not among its four names — and `TestSequencingDriversAreNotAdmitted` pins the
three by name so the decision fails loudly rather than eroding if the allowlist ever grows.
