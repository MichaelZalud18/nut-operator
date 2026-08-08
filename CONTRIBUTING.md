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

Use a writable Go cache when needed:

```sh
GOCACHE=/tmp/go-build-cache make generate
GOCACHE=/tmp/go-build-cache make manifests
GOCACHE=/tmp/go-build-cache make test
```

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

`main` is a protected branch and the protection applies to administrators too. Direct pushes to
`main` are rejected — every change lands through a pull request whose checks pass:

```sh
git switch -c your-change
# ... work ...
git push -u origin your-change
gh pr create --fill
gh pr checks --watch
gh pr merge --squash --delete-branch
```

Five checks are required: `Unit and envtest suites`, `E2E on Kind`, `golangci-lint`,
`ASH security scan`, and `Scan for private IP literals`. Reviews are not required, so a passing PR
can be merged by its author.

This exists because of `F-38`. The E2E gate had correctly caught a bug that made the operator
crash-loop on startup, the workflow sat red for two consecutive commits, and work kept landing on
`main` anyway because nothing enforced the result. Four green badges next to one red one read as
flake. The test was never the problem; the missing piece was that a red gate stopped nothing.

**If CI itself is broken** and you genuinely need to land a fix that its own gate blocks, lift the
protection deliberately and put it straight back — do not leave it off:

```sh
gh api -X DELETE repos/:owner/:repo/branches/main/protection
# land the fix
gh api -X PUT repos/:owner/:repo/branches/main/protection --input .github/branch-protection.json
```

`.github/branch-protection.json` holds the settings, so restoring them is a single command rather
than a click-path someone has to remember.

## Commit Style

Use concise conventional commits when practical:

```text
feat: add node power agent rendering
fix: reject unsupported local UPS drivers
docs: document shutdown flow approval model
```

## Public Safety

Examples should use placeholder domains, placeholder registries, and synthetic cluster names. Do not include real home, business, or customer network topology.
