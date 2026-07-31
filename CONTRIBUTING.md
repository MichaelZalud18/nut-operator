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

## Commit Style

Use concise conventional commits when practical:

```text
feat: add node power agent rendering
fix: reject unsupported local UPS drivers
docs: document shutdown flow approval model
```

## Public Safety

Examples should use placeholder domains, placeholder registries, and synthetic cluster names. Do not include real home, business, or customer network topology.
