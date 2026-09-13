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

`make test-postgres` runs the audit database component suite using Docker and a disposable,
digest-pinned PostgreSQL container. It creates a private network and loopback-only ephemeral port,
then removes its container and network on exit. No existing database is required. The `postgres`
build tag keeps this separate from `make test`; direct tagged runs require an explicit
`AUDIT_TEST_POSTGRES_DSN` pointing at a disposable database where a test-owned schema may be created
and dropped. This suite tests PostgreSQL behavior, not CNPG failover or full outage orchestration.

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
