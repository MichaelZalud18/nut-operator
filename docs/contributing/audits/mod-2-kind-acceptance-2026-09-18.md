# MOD-2 mixed advisory actuation acceptance

Components: Modular Deployment Profiles; Planning & Execution Logic.
Audience: contributors.

Date: 2026-09-18.

## Scope

The approved v1 contract composes existing RunHook groups with built-in agent shutdown.
External targets are explicit receiver data, never fabricated Kubernetes Nodes. Hook delivery
is advisory: HTTP success does not prove host halt, and failures do not gate later groups.
The public [example](../../examples/mixed-hooks/README.md) and
[receiver contract](../../examples/mixed-hooks/receiver-contract.md) describe power-domain
scope, authorization, rehearsal, repeat safety and ordering limits.

## Owned Kind scenario

The existing logical ShutdownFlow TEST-2 scenario now includes five ordered external groups
before its existing scale, drain and Simulate-agent groups. It reuses the suite's pinned Python
fixture image and the existing disposable three-node Kind runner with policy-enforcing CNI.
The receiver has no Kubernetes token, host privileges or actual host operations.

The assertions cover:

- Real admission rejection of unapproved Enforce.
- Secret-backed bearer authentication and explicit external-host data.
- Receiver network policy allowing the manager and denying an unrelated workload.
- Dry-run rehearsal delivery, omission of hooks without rehearsal, and zero receiver effects.
- Two deliveries of an ensure-stopped operation producing one harmless simulated effect.
- HTTP failure, timeout and rejected endpoint allowlist before subsequent built-in actions.
- PostgreSQL action ordering through hooks, scale, drain and agent signal publication.
- Production-rendered Simulate agent receipt, survivor isolation and expired-signal rejection.
- Fixture restoration and owned cluster/kubeconfig cleanup.

The Kind receiver uses isolated HTTP. The separate controller component test exercises
loopback HTTPS with certificate verification. Neither proves a deployed external receiver,
real external-host halt, Linux/Talos guest power-off or full-suite coexistence.

## Validation

Fixture/component checks passed:

```sh
go test -tags=e2e ./test/e2e -run 'TestModularHookFixture|TestLogicalFlow|TestKindSpecRegistration' -count=1
bin/golangci-lint run --build-tags=e2e ./test/e2e/...
make test-modular-components
```

The focused Kind command is:

```sh
python3 -B hack/test-kind.py --focus 'logical ShutdownFlow'
```

**Passed:** one selected Kind spec, 22 skipped; the Go e2e package completed in 583.899 seconds
and the owned runner exited zero. Fixture resources were removed, worker/system placement was
restored, the manager kustomization was restored, and the owned cluster containers and private
state directory were absent after teardown. This is a focused result, not a full-suite pass.

The run built the local dirty checkout, including concurrent planner changes, rather than
qualifying a published image or clean commit. The scenario retains its existing TEST-2
registration and the suite's 23-spec inventory. No Hadron/Talos VM work was involved.

Local image IDs recorded during the run (not registry digests):

| Image | SHA-256 image ID |
| --- | --- |
| nut-operator | `aa84fc2e806ce368d2ac9c1b73b708eff0b6f77372a552aa5570416d692a2375` |
| nut-server | `31372ec0153acfc772089e15d14502fb9521b26784ead6bc30f66032a361023a` |
| upsmon-agent | `b585785a6dedcc7b083f8c3cbf0efada11926180c9aa0ef827375b0f8d7fcf68` |
| node-actuator | `b0a966a6dc7c8a20edc2edebc947eea2fc1bdcac2a414f23eb0e5f9fbcca721e` |
| snmpsim-fixture | `2edabea853b1c217f2e46343db7deb465a6dc16f7dcafd3a3f71ec283cc8fe72` |

The [final MOD-4/MOD-5 regression](mod-4-5-nut-only-acceptance-2026-09-18.md#full-profile-regression)
reran this scenario successfully after selective startup and the TLS rendering fix. It records
the later 24-spec inventory, image identities and successful teardown separately from this run.
