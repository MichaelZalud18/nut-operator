# Scripted UPS Simulation Example

Components: NUT Server / upsd, Node Agent / DaemonSet.
Audience: operators.

Demonstrates `UPSDevice.spec.simulation`: a `dummy-ups` device driven by a scripted
state-transition fixture instead of NUT's static `.dev` file, so `OnBattery`/`LowBattery`
transitions can be exercised end-to-end (through telemetry polling, `ShutdownFlow` trigger
eligibility, and `NodePowerAgent` release gating) without real hardware.

`ups-and-server.yaml` applies:

- A `ConfigMap` holding a `.seq` fixture (NUT's dummy-loop format: `variable: value` blocks
  separated by blank lines, each optionally preceded by a `TIMER <seconds>` line). This fixture
  cycles Online (100% charge) -> OnBattery (40%) -> LowBattery (8%), 20 seconds per state, looping
  back to Online once the sequence ends.
- A `UPSDevice` with `driver: dummy-ups` and `spec.simulation.sequenceConfigMapRef` pointing at
  that ConfigMap, instead of `spec.endpoint` or a static definition.
- A `NUTServer` that selects it by label, matching the `orion-cluster` example's shape.

Apply order: `../cluster.yaml` and `../capability-profile.yaml` first -- the shared
`PowerManagementCluster` this scenario references by name, which also creates the operand namespace,
and the profile the scripted fixture matches against. Then `ups-and-server.yaml`.

What to observe:

- `kubectl get upsdevice orion-core-ups-simulated -o jsonpath='{.status.phase}'` cycles through
  `Online`, `OnBattery`, `LowBattery` roughly every 20 seconds, sourced from the real
  `UPSDeviceReconciler` telemetry poller reading the rendered NUT server's `upsc` output --
  not a mock.
- A `ShutdownFlow` with an `OnBattery` or `LowBattery` trigger targeting this device's
  `powerDomains` becomes eligible on the same cadence, letting the full trigger -> compile -> wave
  pipeline be driven by a scripted fixture in a repeatable test rather than by hand-patching
  status.

Authoring a custom fixture: write any number of `variable: value` blocks (separated by a blank
line) into the ConfigMap's `sequence.seq` key (or a custom key referenced by
`spec.simulation.sequenceKey`), each preceded by an optional `TIMER <seconds>` line. Values follow
the same NUT variable names as the static fixture (`ups.status`, `battery.charge`,
`battery.runtime`, `ups.load`, ...). `ups.status` accepts NUT's space-separated status flags (for
example `OB LB` for on-battery-and-low, not just a single token).

Scope note: this exercises the `dummy-ups` driver's own scripted playback and this project's
telemetry/trigger pipeline. It says nothing about whether a real vendor's `snmp-ups` OIDs are
being read correctly -- that is a separate conformance question, covered by the `snmpsim` fixture
under `../snmpsim`.
