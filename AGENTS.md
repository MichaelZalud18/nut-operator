# nut-operator Agent Guide

This repository is a Kubebuilder operator. Keep changes small, generated files generated, and
design decisions grounded in the existing docs.

## Read First

- `README.md` for the public shape of the project.
- `docs/contributing/design/settled-questions.md` before proposing or changing a design decision.
- The owning design doc for the component you are touching.

Do not mint a new `OD` number until you have checked the settled questions and the relevant
requirement IDs.

## Project Shape

- `api/<version>/*_types.go`: CRD schemas and Kubebuilder markers.
- `internal/controller/`: reconcilers and renderers.
- `internal/webhook/`: validation and defaulting.
- `internal/planner`, `internal/executor`, `internal/kubeactions`, `internal/shutdownflow`: shutdown
  planning and execution.
- `cmd/`: manager and operand/helper binaries.
- `config/`: Kubebuilder-owned manifests and Kustomize overlays.
- `dist/`: generated install bundles.
- `docs/`: public-safe product, operator, reference, and contributor docs.

## Generated Files

Do not hand-edit:

- `config/crd/bases/*.yaml`
- `config/rbac/role.yaml`
- `config/webhook/manifests.yaml`
- `**/zz_generated.*.go`
- `PROJECT`

Do not remove `// +kubebuilder:scaffold:*` comments.

For new APIs or webhooks, use Kubebuilder CLI scaffolding instead of manually creating the generated
shape.

## Design Rules

- Reconciliation must be idempotent.
- Shutdown actions must be safe to repeat. Executor restart/resume continuity and exactly-once
  execution are out of scope; see [SB-1](docs/contributing/design/scope-boundaries.md#executor-restarts-and-idempotency)
  before opening a resume-evidence or execution-ID finding. Existing resume code is not a scope
  commitment.
- Re-fetch before status/spec updates where conflicts are possible.
- Use owner references for rendered resources.
- Watch secondary resources explicitly with `.Owns()` or `.Watches()` rather than relying only on
  polling.
- Keep status current-state oriented. Durable execution history belongs in PostgreSQL/audit storage.
- Keep NUT credentials, Kubernetes API access, and host actuation privileges separated.
- Do not weaken the actuator boundary without updating the security docs and tests.

## Validation

After editing API types or Kubebuilder markers:

```sh
make manifests generate
```

After editing Go code:

```sh
make lint-fix
make test
```

Useful narrower checks:

```sh
go test ./api/... ./cmd/... ./internal/... ./test/utils -count=1
make vet
git diff --check
```

E2E tests must run against an isolated Kind cluster, never a real dev/prod cluster:

```sh
make test-e2e
```

The multi-node Kind suite needs the host inotify limit checked by `make check-test-e2e-host`.

## Distribution

Install bundles and catalog bundles are generated from source:

```sh
make build-installer build-catalog IMG=<registry>/nut-operator:<tag>
```

Commit generated bundles only after verifying they match the source state being published.

## Documentation

- Root `README.md`: product shape, safety model, install entry point.
- `docs/`: public-safe concepts, installation, guides, reference, examples, troubleshooting, and
  contributing docs.
- `docs/tasks.md`: open v1 work only.
- `docs/tasks-post-v1.md`: real work deliberately deferred by scope or upstream gates.
- `docs/contributing/audits/`: dated evidence and finding history.

Do not place private infrastructure details, private hostnames, private IPs, credentials, or
site-specific topology in public docs.

## Logging

Use Kubernetes logging style: active voice, capitalized messages, no trailing period, and balanced
key/value pairs.
