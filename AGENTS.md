# nut-operator Agent Guide

This is the reusable product development repository, not a site deployment checkout.
Keep guidance procedural; link to authoritative files instead of copying inventories or status.

## Read First

- `README.md` for the public shape of the project.
- `docs/contributing/design/settled-questions.md` before proposing or changing a design decision.
- `docs/contributing/design/scope-boundaries.md` for explicit exclusions.
- The owning design doc for the component you are touching.
- Relevant tasks, recent commits, and working-tree changes before starting work or reporting
  progress. Preserve concurrent edits and do not attribute another session's work to yourself.
- Check CI when the changes or question warrant it, matching results to the revision and test
  scope they actually cover. Historical audit findings are not proof of current defects.

Do not mint a new `OD` number until you have checked the settled questions and the relevant
requirement IDs.

## Sources of Truth

- Use `docs/README.md` for documentation navigation and the owning design contract for behavior.
- Use `Makefile` for build, generation, and test entry points; `.github/workflows/` and referenced
  configuration own CI implementation and tool pins.
- Verify live repository state when branch rules, published artifacts, or remote checks matter.
  Checked-in configuration is not proof that remote settings have been applied.
- Do not maintain commit lists, CI snapshots, dependency versions, capacity figures, or claims
  about an agent's tool access in guidance. Keep exact facts with configuration or dated evidence.

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

Follow [CONTRIBUTING.md](CONTRIBUTING.md#development-checks). Inspect build tags and entry points
so default Go tests do not silently omit the component being changed.

Distinguish unit/component tests, Kind workloads, VM boot checks, and guest-shutdown acceptance.
A pass in one layer is not evidence for another; mocks or terminating a VM process do not prove
that an actuator shut down a guest. Report what ran and what remains unverified.

Use isolated test resources, never an existing deployment by default. Before destructive tests,
verify the authorized cluster, VM identities, and cleanup ownership. An infrastructure limitation
is not a product-test pass or automatically a product defect.

## Distribution

Generate installer and catalog bundles through the Makefile, not manual edits. Verify they match
the source state being published.

Commit, push, install tooling, deploy, or actuate only with explicit authorization. Task checkboxes,
instructions, and available credentials do not grant it. Never weaken repository protections merely
to make a change land.

## Documentation

- Root `README.md`: product shape, safety model, install entry point.
- `docs/`: public-safe concepts, installation, guides, reference, examples, troubleshooting, and
  contributing docs.
- `docs/tasks.md`: active v1 engineering/component work and scoped investigations.
- `docs/tasks-v1-release.md`: v1 release readiness, validation gates, and publishing mechanics.
- `docs/tasks-post-v1.md`: real work deliberately deferred by scope or upstream gates.
- `docs/tasks-completed.md`: completed task records with original dates, evidence, and scope limits.
- `docs/contributing/audits/`: dated evidence and finding history.

Keep active task entries focused on remaining work and acceptance. Move completed entries to the
completed tracker; link substantial research and milestone history from the owning design/audit
document. Proposals belong in clearly labeled research, not the settled decision registry.

Task prefixes follow the owning component/section, not the type of work. Reuse its established
namespace and related requirement ID where applicable; do not introduce project-wide prefixes
such as `ENG` or `TEST` merely to distinguish coding from testing. Consult the
[decision index](docs/contributing/design/decision-index.md) before assigning an ID. Preserve
existing identifiers and historical references; renaming them requires an explicit migration.

Do not place private infrastructure details, private hostnames, private IPs, credentials, or
site-specific topology in public docs.

## Logging

Use Kubernetes logging style: active voice, capitalized messages, no trailing period, and balanced
key/value pairs.
