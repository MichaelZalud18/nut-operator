# NUT Source Patches

The Dockerfile verifies the pinned upstream source archive and signature before applying these
patches. Keep them narrow and review each one when changing `NUT_VERSION`; do not silently drop
a patch merely because its context changed.

## Require PONG

`upsdrvquery-require-pong.patch` fixes `upsdrvquery_prepare` treating a PING timeout as a successful
handshake in NUT 2.8.5. Only an actual PONG permits preparation to succeed. This also makes other
users of that handshake fail closed instead of proceeding after silence.

The production readiness helper still bounds the entire operation and probes devices concurrently:
this patch alone does not bound upstream connect latency or serial multi-device queries. It does
not change driver process supervision, device polling, credentials, or privileges.

Source: [pinned upstream implementation](https://github.com/networkupstools/nut/blob/v2.8.5/drivers/upsdrvquery.c).
Background: [NS-1 investigation](../../../docs/contributing/audits/nut-readiness-investigation-2026-09-17.md).
The patched upstream code remains under NUT's GPL license; this patch does not relicense NUT.

## Preserve PONG Framing

`upsdrvquery-pong-framing.patch` preserves line state across socket reads and scans only bytes
actually received. A valid PONG split across reads still succeeds; a malformed line ending in
PONG cannot succeed merely because the final fragment starts at a read boundary. Matching uses
constant space, and only a complete exact PONG line succeeds. The real-protocol regression suite
checks valid and invalid fragmentation in addition to silence and incomplete replies.
