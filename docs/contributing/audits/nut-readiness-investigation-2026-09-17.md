# NUT Readiness Investigation

Scope: F-97. Source review and disposable-container experiments on September 17, 2026.
This does not claim the historical startup-only failure is reproduced or fixed.

**Task scope superseded 2026-09-17:** [NS-1 and NS-6](../../tasks.md#nut-server--upsd)
replace F-97. NS-1 owns readiness correctness; NS-6 verifies startup under the new supervisor
as part of ENG-1 acceptance. The historical closure criteria and experiment ideas below are
research context, not a requirement to solve the old watchdog's root cause after a clean
scoped current-system verification. [Superseded task record](../../tasks-completed.md#superseded-tasks).

## Findings

1. **High, confirmed upstream classification defect:** pinned NUT 2.8.5 can print `RESPONSIVE`
   for a kernel-confirmed stopped driver. A three-second PING timeout is accepted by
   `upsdrvquery_prepare`; `status_driver` converts that result into the responsive flag even
   when GETPID also times out. The observed row had `S_PID=N/A`, empty status, and exit zero
   after 7.007 seconds. The rendered five-second Kubernetes deadline limits this particular
   failure shape, but does not prove every false-positive response takes longer than five seconds.
2. **Confirmed evidence limitation:** `upsc` returned `OL` while `/proc/<pid>/status` confirmed
   the driver was stopped. Stored values and open connections do not prove current driver
   responsiveness. The historical eight disagreements without upsd disconnects therefore do
   not establish that those drivers were healthy. They do not prove they were stalled either.
3. **Confirmed harness gap:** the existing stress test starts after membership, reload, and
   port-replacement checks. It samples a warmed `.dev` fixture with one authenticated upsmon,
   not the first eleven minutes of startup with five/eight monitors. Four concurrent upsc
   clients connect to upsd, not four extra driver-socket sessions.
4. **Current scope:** the Go supervisor owns foreground process exits; it does not restart
   workers because status probes fail. The old probe-driven restart loop is historical.
   Trustworthy readiness and stalled-driver behavior remain relevant independently of exits.

## Upstream Trace

The pinned [status implementation](https://github.com/networkupstools/nut/blob/v2.8.5/drivers/upsdrvctl.c)
opens a driver socket, prepares it, requests PID, then requests status. Each response phase uses
three seconds. Its responsive column depends on preparation, not successful PID/status retrieval.
Devices are visited serially: a slow first device can exhaust the Kubernetes deadline before a
later healthy device is examined.

In [upsdrvquery.c](https://github.com/networkupstools/nut/blob/v2.8.5/drivers/upsdrvquery.c),
preparation sends NOBROADCAST and PING. A ping timeout goes to the same successful return as a
PONG. Closing deliberately waits one second after LOGOUT. Connection setup uses blocking
`connect` before enabling nonblocking mode. These are separate mechanisms: handshake
classification, cumulative latency, and potentially blocked connection setup.

Upstream [1173d34c](https://github.com/networkupstools/nut/commit/1173d34c83744ee17d0d4c92fa7bed42b6f4b0cc)
replaces the blocking connection with a bounded helper. Its stated concern is an unresponsive
driver exhausting the listen backlog. This change is absent from the inspected 2.8.5 source.
It is relevant, not proof of F-97's cause. At inspected upstream head
[fe4e6fec](https://github.com/networkupstools/nut/blob/fe4e6fec3807017418c6fbc19b007a660d8e409c/drivers/upsdrvquery.c),
the PING-timeout success fall-through remains. Upgrading alone is not an established fix.

[upsd's state layer](https://github.com/networkupstools/nut/blob/v2.8.5/server/sstate.c)
maintains a variable tree and last-heard timestamps. Its default
[MAXAGE](https://networkupstools.org/docs/man/upsd.conf.html) window is 15 seconds. This explains
how a stopped driver and a successful immediate client read can coexist. Liveness experiments
need advancing data or an observed driver reply, not repeated reads of constant OL.

[dummy-ups](https://github.com/networkupstools/nut/blob/v2.8.5/drivers/dummy-ups.c) sleeps one
second in its update function. TIMER records set a next-update timestamp and leave parsing;
they do not simply sleep for that entire duration. CPU starvation and update scheduling remain
hypotheses, not reasons to blame TIMER or increase timeouts without evidence.

## Reproduction And Results

The opt-in diagnostic uses the existing non-root, read-only, networkless supervisor fixture.
It pauses only its owned dummy driver, confirms kernel stopped state, tests cached reads and
the five-second command deadline, records raw debug classification, then resumes the same PID.
The enclosing 180-second timeout and container deletion remain the final isolation boundary.
This is an injected control, not spontaneous reproduction or a running-kubelet test.

```bash
NUT_READINESS_DIAGNOSTICS=1 make docker-smoke-nut-supervisor NUT_SERVER_IMG=<image>
```

Test image: local ARM64 NUT 2.8.5,
`sha256:8259f3eaacd9590ffa3254519d213783225e1b8ee311eab1da795b30010af9d4`.
Its revision label is `unknown`; this does not qualify a promoted digest or another architecture.

| Attempt | Outcome |
| --- | --- |
| Initial negative-control assertion | Failed: stopped driver unexpectedly printed RESPONSIVE after 7.007s; upstream defect captured |
| Added five-second deadline | Harness failed: assumed GNU timeout exit 124, but operand BusyBox returns signal status |
| Corrected timeout convention | Passed: deadline rejected paused driver; longer probe printed false-positive after 7.008s; same PID recovered; complete supervisor smoke passed |
| Review-strengthened recovery check | Passed again: fresh socket PID matched the resumed driver within the bounded command; complete supervisor smoke passed |

Failed attempts are retained here, not counted as successful runs. No production probe,
restart policy, upstream source, or host settings were changed. The diagnostic recognizes
both responsive/nonresponsive upstream classifications and reports which it observes; passing
means the control and recovery worked, not that upstream readiness is correct.
Existing readiness component tests and shell syntax checks also passed. Independent review
approved the final diagnostic, and a post-run Docker check found no surviving fixture containers.

## Remaining Experiments

- **High:** qualify false positives with partial/delayed replies inside five seconds. Cover
  absent sockets, frozen drivers, missing PID responses, healthy-device-last ordering, and total
  deadline. Review a narrow upstream fix or stronger readiness contract. Cached upsc reads and
  restoring probe-driven restarts are not substitutes.
- **High:** capture a spontaneous startup failure with driver debug, exact probe stderr/time,
  socket identity, child exits, CPU throttling counters, and cgroup limits. Observe from launch
  through at least the historical eleven-minute window; retain failures and bound cleanup.
- **Medium:** compare zero/five/eight authenticated monitors, stable versus reconnecting clients,
  `.dev` versus `.seq`, and default versus constrained CPU. Vary one input per paired run with
  identical source/image/configuration identities. Test ARM64 and AMD64 separately.
- **Medium:** compare pinned NUT with the reviewed bounded-connect change, including a filled
  driver listen backlog. Keep this an isolated build, not an implicit production dependency bump.

F-97 closure needs a captured spontaneous mechanism and a regression test that fails before its
selected fix. Readiness classification needs separate explicit verification. No physical UPS is
required for these dummy-driver/socket questions; device compatibility remains a separate boundary.
