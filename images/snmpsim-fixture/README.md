# snmpsim UPS-MIB fixture

A `snmpsim-command-responder` image serving a static RFC1628 UPS-MIB tree, used by the e2e suite to
prove the real `snmp-ups` driver decodes a real SNMP device correctly.

Test-only. It is deliberately excluded from `make docker-build-operands` and from `images.yml`'s
publish matrix — it is never an operand and is never published.

## What it proves, and what it does not

Proves **OID and decode conformance**: that `snmp-ups` reads the UPS-MIB tree and maps it onto the
NUT variables the operator consumes. The fixture's OIDs and scaling factors were taken from NUT's own
`drivers/ietf-mib.c` mapping table rather than guessed, then confirmed by running the real driver
against the simulator in dump mode and reading back `battery.charge: 100`, `battery.runtime: 3600`,
`ups.load: 10`, `ups.status: OL`, plus `ups.mfr`/`ups.model`.

Does **not** prove authentication parity. The fixture serves SNMPv2c community auth only, while
production hardware generally requires SNMPv3 (`secName`/`authPassword`/`privPassword`, wired through
`UPSDevice.spec.credentialSecretRef`).

Does **not** drive state transitions — the tree is static. Scripted `Online`/`OnBattery`/`LowBattery`
transitions are the `dummy-ups` fixture's job (`UPSDevice.spec.simulation`, see
`docs/examples/simulation/`). The two are intentionally not conflated: `snmpsim` proves the driver
talks to real OIDs, `dummy-ups` proves the operator reacts once a device reports state.
