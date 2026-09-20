# MOD-5 component acceptance milestone

Components: Modular Deployment Profiles.
Date: 2026-09-18.
Task owner: [MOD-5](../../tasks.md#modular-deployment-profiles).

Implemented the [modular component target and coverage map](../design/modular-acceptance.md).
The new mixed-flow test connects production reconciliation, authored inventory resolution,
trigger evaluation, planning, execution, local HTTPS delivery and worker signal publication.
Kubernetes and audit storage are fakes; no actuator, Kind cluster or VM runs in this fixture.

Nine scenarios passed: delivery, repeated operation under a new flow identity, HTTP failure,
timeout, allowlist denial, dry-run, explicit rehearsal, unapproved flow and agent approval
revoked during the preceding hook. Assertions check delivery/publication ordering, advisory
failure evidence, repeat-safe fake receiver effects, exact signal destination and absence of
signals for denied operations. Existing tests supply freshness, targeting and other safety
checks rather than duplicating them in a new subset matrix.

Validation:

- `make test-modular-components` passed.
- `go test -race ./internal/controller -run '^TestModularMixedFlowAcceptance$' -count=1` passed.
- Affected package suites `./internal/controller ./internal/kubeactions ./test/quickstart`
  passed, with the controller suite using the already-installed 1.36.2 envtest assets.
- `bin/golangci-lint run ./internal/controller/...` passed after extracting the test assertions
  into a helper; no lint suppressions were added.
- `git diff --check` passed. The local Tests workflow now includes mixed-hooks fixture changes;
  no remote CI run is claimed.

MOD-5 remains open. US-2 still requires the owned-Kind mixed hook/Simulate-agent integration.
US-1, US-3 and US-4 install acceptance depends on their selected contracts and implemented
packages; managed relay also depends on NS-11. No profile decision, production storage
exception, new authorization API or supported-install claim was introduced. Existing
Hadron/Talos and shared planner edits were preserved.
