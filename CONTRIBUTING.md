# Contributing

Thanks for helping improve NUT Operator.

This project is early-stage and security-sensitive. Contributions should keep the operator Kubernetes-native, public-safe, and conservative around host power actions.

## Ground Rules

- Do not commit credentials, kubeconfigs, private IP addresses, private hostnames, or site-specific overlays.
- Keep real node shutdown behavior gated behind explicit dry-run, approval, and actuator isolation controls.
- Do not add local USB or serial UPS support to the default path. Network-reachable UPS devices are the supported baseline.
- Keep generated files generated: update API markers and run the Kubebuilder generation targets instead of hand-editing generated CRDs or DeepCopy files.
- Prefer small, reviewable changes with tests for controller behavior and API validation.

## Development Checks

Use the current `Makefile` for targets and tool versions. For Go changes:

```sh
make lint-fix
make test
```

After changing API types or Kubebuilder markers:

```sh
make manifests generate
```

`make test` sets up the required envtest assets. Use a writable `GOCACHE` when needed;
do not mistake a sandbox restriction for a broken host setup.

`make build-installer-nut-only` derives the standalone NUT profile from the canonical generated
BYO-cert resources. Its packaging/component tests run in `make test` and
`make test-modular-components`; the latter does not establish live installation behavior.
Run `CERT_MANAGER_INSTALL_SKIP=true python3 -B hack/test-kind.py --focus 'NUT-only'` for the
clean profile's real TLS/client-policy, lifecycle and upstream-relay acceptance. The scenario
is serial and uses only owned disposable resources, with no VM or host actuation.

`make test-postgres` runs the audit database component suite using Docker and a disposable,
digest-pinned PostgreSQL container. It creates a private network and loopback-only ephemeral port,
then removes its container and network on exit. No existing database is required. The `postgres`
build tag keeps this separate from `make test`; direct tagged runs require an explicit
`AUDIT_TEST_POSTGRES_DSN` pointing at a disposable database where a test-owned schema may be created
and dropped. This suite tests PostgreSQL behavior, not CNPG failover or full outage orchestration.

`make test-operand-deletion` creates a disposable, single-node Kind cluster with a private
kubeconfig, runs focused controller deletion/migration specs, and removes that cluster on exit.
It proves real Kubernetes garbage collection without installing the operator or exercising host
actuation. Ordinary envtest tests verify owner references and API deletion but do not run GC.

`make test-e2e` owns a fresh Kind cluster and a private temporary kubeconfig for the entire run.
`KIND_CLUSTER` is a name prefix, not permission to reuse or delete an existing cluster. The runner
checks context and cluster UID before setup and the Go suite; cleanup deletes only verified full
node container IDs, leaving the shared Kind network alone. The normal kubeconfig/context stays
unchanged. Standalone setup/cleanup
targets refuse name-only operations; invoke the complete runner instead. On cleanup failure,
inspect the reported private state directory and cluster identity before manual removal. Do not
publish that directory: it contains credentials. Cancellation cleanup is bounded, but cannot be
guaranteed after runner loss or SIGKILL. `make test-kind-harness` exercises ownership, failure,
and cancellation paths without Docker, Kubernetes, or VMs. Python 3 is required by the runner.

For focused local iteration, run from the repository root:

```sh
python3 -B hack/test-kind.py --focus 'should run successfully|logical ShutdownFlow|NS-6'
```

Execution-safety integration has a separate focused selection:

```sh
python3 -B hack/test-kind.py --focus 'EX-34 checks execution safety'
```

It uses the normal certificate-manager fixture, real dummy-ups telemetry, an observable hook
barrier, PostgreSQL evidence and Simulate agents. It covers a positive baseline, an admitted
flow-mode change and a selected-agent generation change, following the original execution ID.

This explicit CLI option forwards one nonempty regexp to Ginkgo, which validates Go regexp
syntax, and enables `-ginkgo.fail-on-empty` so an unmatched focus fails. Focused runs are not
full acceptance. They retain the shared `BeforeSuite` image setup,
host preflight, private cluster ownership checks, CNI setup, timeouts, and cleanup. No arguments
still runs the unfiltered suite; `verify` is unchanged. The existing
`NUT_OPERATOR_E2E_STARTUP=true` opt-in is still required to execute NS-6 rather than skip it.

`make test-e2e-nut-startup` adds the NS-6 eleven-minute startup observation to that same owned
Kind suite. It extends the suite timeout to 60 minutes (65 minutes including cluster setup),
while preserving the independent cleanup deadline. The ordinary suite allows 45 minutes,
including the three isolated EX-34 scenarios and their fixture teardown.
The E2E Tests workflow also exposes this option through manual dispatch; it is not added to every
pull request or image-promotion run. Keep the full window for acceptance; component Docker startup
observations do not substitute for manager/kubelet evidence.

`make test-kind-lifecycle` rehearses cancellation during partial startup and after API ownership
is established, using the same owned runner and host preflight. It checks observed container
cleanup and unchanged external kubeconfigs; failures retain private evidence for inspection.
This opt-in rehearsal and its manual CI workflow supplement the full E2E/image gate.
`make test-kind-lifecycle-harness` tests the rehearsal's checks without starting clusters.

`make test-netbox` runs the shipped inventory importer against a disposable real NetBox service.
Its isolated Docker resources and synthetic inventory belong to the test; no site NetBox is needed.
See the [NetBox guide](docs/guides/import-netbox-inventory.md) for the fixture contract.
`make test-netbox-harness` checks the service runner without starting containers. This service
suite is separate from Kind and from shutdown execution.

### Test dependency pinning

Images being promoted must remain immutable and digest-addressed through acceptance tests.
Semantics-affecting test infrastructure uses explicit stable versions, with a digest or checksum
where artifact identity matters (for example, the Kind node image). Small gating helpers use
versioned releases; additional digests/checksums are optional unless exact artifact identity is
required. `latest` is reserved for explicitly non-gating development or compatibility checks.
Keep upgrades routine. Actual versions belong in the owning Makefile, workflow, or test fixture,
not duplicated in guidance.

### Selecting checks

`make test-modular-components` runs the available modular composition and dependency tests,
including controller-to-HTTPS-hook-to-agent-signal ordering, negative authorization cases,
the authored mixed-flow example, and the restricted NUTServer render probe. It uses fake
Kubernetes/audit storage and a loopback TLS receiver; no cluster, guest or actuator is started.
These tests also run under `make test`. It does not qualify an unimplemented install profile;
see [modular acceptance coverage](docs/contributing/design/modular-acceptance.md).

Inspect build tags and test entry points when selecting narrower checks. Component tests,
Kind suites, and opt-in VM tests have different prerequisites and prove different things.
Read the relevant workflow before running infrastructure tests, and use isolated resources,
never an existing deployment by default. See
[test domains](docs/contributing/design/test-domains.md) for what each layer actually proves,
which files and workflows own it, and what nothing yet proves.

For the AWS Labs Automated Security Helper scan, use the installation/version configuration
in `.github/workflows/security.yml` rather than maintaining a second pin here:

```sh
make security-scan
make security-triage
```

The `Makefile` owns scanner pins and exclusions. A skipped scanner is not a passing scan;
`security-triage` reports findings from the last scan, not necessarily the current revision.

Useful manifest checks:

```sh
kubectl kustomize config/default
kubectl kustomize config/samples
yamllint config/samples docs/examples
```

Before opening a pull request:

- Run `git diff --check`.
- Run the relevant Go tests.
- Confirm generated manifests are current after API changes.
- Confirm examples do not contain private infrastructure details.

## Landing Changes

Review recent commits and the working tree first. `main` is a protected branch; every change
lands through a pull request:

```sh
git switch -c your-change
# ... work ...
git push -u origin your-change
gh pr create --fill
gh pr checks --watch
gh pr merge --squash --delete-branch
```

`.github/branch-protection.json` holds the required-check configuration — treat it as what should
be applied, not proof of what is currently live; verify current settings on GitHub rather than
trusting the checked-in file, and don't duplicate the specific check list here where it can drift
out of sync. Reviews are not required, so a passing PR can be merged by its author.

Check CI results against the specific revision being landed. A skipped or unrelated green job does
not validate a change; diagnose a broken required check rather than working around it — changing
branch protection is a separate, deliberate maintainer decision.

**If CI itself is broken** and a fix genuinely needs to land through its own blocked gate, lift
protection deliberately and put it straight back:

```sh
gh api -X DELETE repos/:owner/:repo/branches/main/protection
# land the fix
gh api -X PUT repos/:owner/:repo/branches/main/protection --input .github/branch-protection.json
```

## Commit Style

Use concise conventional commits when practical:

```text
feat: add node power agent rendering
fix: reject unsupported local UPS drivers
docs: document shutdown flow approval model
```

## Public Safety

Examples should use placeholder domains, placeholder registries, and synthetic cluster names. Do not include real home, business, or customer network topology.
