# Security

Components: Cross-cutting.

`nut-operator` manages infrastructure that can shut down physical nodes. Security is part of the product contract, not a deployment afterthought.

## Defaults

- Real actuation is off by default.
- `NodePowerAgent` starts in `DryRun`.
- `ShutdownFlow` starts in `DryRun`.
- Real host shutdown requires an approval annotation on the exact resource that requests enforcement.
- Durable state defaults to PostgreSQL/CNPG, not local files.
- Non-actuator containers run as non-root with read-only root filesystems and `RuntimeDefault` seccomp.
- UPS connectivity is network-only by default. Local USB and serial UPS access are unsupported, so NUT server and monitor containers do not require host device mounts or privileged mode.
- UPS drivers are accepted through an explicit network-driver allowlist. Unknown drivers are rejected until reviewed.
- Appliances that expose their own NUT server use explicit upstream relay configuration and still render as non-privileged network-only pods.

## Privilege Boundary

The host-action boundary is intentionally narrow.

The NUT server and client containers use network UPS protocols only. They do not need broad Linux capabilities, host devices, host namespaces, or Kubernetes API tokens.

The actuator container owns host interaction only when approved actuation is enabled. It has no NUT credentials, no flow logic, and no broad policy authority. Its job is to validate the signal and execute the approved local action.

One signal path is authorized, and it is the executor-projected Secret, mounted read-only (`OD-37`). The shared `power-agent-run` tmpfs that `upsmon` writes its `SHUTDOWNCMD` handoff into is **not mounted into the actuator at all**. That is deliberately stronger than mounting it read-only: the halt comes from the actuator *reading* a path, so restricting writes would have closed a threat nobody posed while leaving the read open. A volume the container does not mount is a volume it cannot be tricked through. The actuator's default signal path is derived from `POWER_NODE_NAME` rather than fixed, so a failed environment injection leaves it watching nothing; the previous default was the local tmpfs path, which meant the same failure silently repointed it at the one path this decision declines to trust. The Secret always carries a `delivery-channel` marker key, because an empty Secret projects identically to a missing one and without a key that is always present there is no way to tell "no flow is running" from "this channel does not exist".

The cost is accepted rather than engineered away, and is recorded as `SB-3`: an undeliverable signal leaves nodes running until the UPS dies. A local backstop would engage exactly when the operator is unreachable, which is when ordering matters most, and `MINSUPPLIES 1` on every agent would release a UPS's entire coverage at once. Full treatment in [the node-agent design](../contributing/design/node-agent-operand.md).

This boundary is testable, and proving it holds is part of reviewing it. `make verify-actuation NODE=<node> AGENT=<agent> APPROVE=yes` renders the real actuate configuration on one named node and delivers a real signal through the projected Secret. It establishes what inspection cannot: that kubelet admits the pod under this cluster's Pod Security Admission, that the binary's `cap_sys_boot` file capability survived the image build and registry round-trip, that it can be raised from permitted into effective, and — the one with no other check available — that the container is genuinely in the host PID namespace, since from a non-initial namespace `reboot(2)` returns success and does nothing. It powers the node off and leaves it off; the full procedure is in [Enabling actuation](../guides/enable-actuation.md#proving-the-cluster-can-halt-a-node).

Approved `PowerOff` rendering uses `hostPID` and adds only `CAP_SYS_BOOT` to the actuator
container. It remains non-root, drops all other capabilities, keeps privilege escalation disabled,
uses a read-only root filesystem, and receives no Kubernetes service-account token. It runs under the
pod's `RuntimeDefault` seccomp profile with no override (`F-62`). An earlier revision set
`Unconfined` on the assumption that the runtime default blocks `reboot(2)`; measurement showed it
does not — with the capability held the syscall reaches the kernel's handler, and without it the
refusal is on the capability. `CAP_SYS_BOOT` is the gate, so `Unconfined` bought nothing while
removing every other syscall filter from the one container that can halt the machine.

## Admission webhook certificate

The operator serves admission webhooks, and admission is load-bearing here rather than cosmetic. Which
certificate path to install with, and the commands for each, are in
[Choosing a webhook certificate path](../installation/webhook-certificate.md). This section is the
reasoning behind that recommendation.

**The trust model is the same on every supported path.** Webhook certificates are not validated
against any public or cluster trust store. The API server validates the webhook's certificate against
`.webhooks[].clientConfig.caBundle` on the webhook configuration object, which only a cluster
administrator can write. A privately issued CA is therefore the correct shape, not a compromise — the
bundled `config/certmanager/issuer.yaml` is itself a `selfSigned: {}` `Issuer`. What differs between
paths is what has to be *working* for admission to work, and that is an availability question rather
than a trust one.

**The CA key's exposure model.** `hack/webhook-cert.sh` stores the generated CA key in a cluster
Secret. Read access to that Secret is enough to mint a certificate this webhook's `caBundle` trusts —
the same exposure cert-manager and `cert-controller` both carry. `--ca-cert`/`--ca-key` sign from an
existing CA instead, so the key never lives in the cluster.

**Defaulting is not duplicated, so removing the webhooks is not a safe shortcut.** The
`NodePowerAgent` defaulter is the only thing that sets `spec.resources.upsmon` and
`spec.resources.actuator`. Without it the tier-0 DaemonSet pods land in BestEffort QoS with no
OOM-score protection or scheduler reservation (`F-34`), and `spec.placement.priorityClassName` has no
CRD-level default to fall back on. Validation is duplicated in the controllers as a second layer;
defaulting is not.

**In-process generation was declined.** Some operators generate and rotate their own serving
certificate in-process, most commonly with
[`open-policy-agent/cert-controller`](https://github.com/open-policy-agent/cert-controller) — the
library Gatekeeper, Kueue, JobSet, LeaderWorkerSet, MetalLB, KEDA, Azure Workload Identity, and the
Kubeflow operators all use. It is a legitimate, widely deployed option. The reasons it is not the
default here are specific to this operator rather than objections to the library:

- **The certificate is generated after the pod starts.** That is a window — Kueue documents 10–30
  seconds — in which the webhook is not yet serving HTTPS. Every webhook here is
  `failurePolicy: Fail`, so during it, creates and updates of this operator's custom resources are
  rejected, and a manager pod rescheduled mid-outage hits that window at the worst possible time.
  With a static Secret the pod does not start at all until a valid certificate exists: the failure is
  loud and up front rather than a brief, quiet rejection window.
- **It requires cluster-scoped write on admission configuration.** Injecting `caBundle` in-process
  means `update` on `validatingwebhookconfigurations` and `mutatingwebhookconfigurations`. That can be
  narrowed with `resourceNames` to this operator's own two objects, but it is still a privilege the
  operator does not hold today, and admission configuration is a high-value target.
- **The trust model is identical.** In-process generation would not weaken anything — but it would not
  strengthen anything either, so a removed install-time dependency is the whole of what is bought.

The bring-your-own-certificate path removes that same install-time dependency without the startup
window or the extra RBAC, which is why it is the recommendation instead.

## RBAC Scope

The manager's ClusterRole is generated from each reconciler's `+kubebuilder:rbac` markers
(`internal/controller/*_controller.go`) via `make manifests`, into `config/rbac/role.yaml`. The
grants below are broader than they might look in isolation and are called out explicitly here so they
aren't mistaken for scope creep in a future audit:

- **`ShutdownHook` read access plus outbound HTTP delivery.** `RunHook` reads a namespaced
  `ShutdownHook` and delivers its HTTP/CloudEvents invocation only when the URL matches
  `PowerManagementCluster.spec.hooks.allowedEndpoints`. Secret-backed headers are read from
  Kubernetes Secrets; sensitive headers such as `Authorization` are rejected when supplied inline.
  Generated RBAC does not grant `argoproj.io/workflows`: Argo is no longer a built-in route.
- **`namespaces` (`create;update;patch`).** `NUTServer`/`NodePowerAgent`/`PowerManagementCluster`
  are cluster-scoped CRDs whose `spec`-referenced operand namespace may not exist yet; the operator
  creates and labels it on first reconcile. Kubernetes RBAC can't scope a `create` verb by resource
  name — only verbs acting on an object that already exists support `resourceNames` — so this can't
  be narrowed further at the RBAC layer. The resulting gap (a CR pointing its operand namespace at a
  reserved system namespace) is closed at the input layer instead: `validateOptionalNamespace`
  (admission webhook) and `rejectReservedOperandNamespace` (controller, belt-and-suspenders) reject
  `default`/`kube-system`/`kube-public`/`kube-node-lease` before the operator ever creates or
  relabels a namespace.

## Network Controls

Generated operands are compatible with default-deny namespaces.

Expected policy edges:

- agent to `NUTServer` on TCP 3493
- operator to Kubernetes API
- operator and audit writer to PostgreSQL
- Prometheus to metrics endpoints when enabled
- DNS only where needed

`NUTServer` is not externally exposed by default.

## Credential Controls

- NUT server users are operator-managed by default.
- Per-node secondary users are preferred over a shared fleet credential.
- Existing Secrets are supported for organizations with external secret management.
- SNMP credentials and PostgreSQL DSNs must always come from Secrets.
- Upstream NUT relay credentials use Secret-projected `nutauth.conf` files; unauthenticated appliances must explicitly use `auth.mode: None`.
- Generated credentials must be rotatable without recreating API objects.

## TLS

NUT protocol TLS defaults to `Required` in the API contract. Renderers require mounted certificate material before required TLS mode is ready.

PostgreSQL TLS is required for external PostgreSQL by default.

## Policy Gates

Unsafe behavior is blocked in three places:

1. CRD schema where possible.
2. Admission webhooks for object-local unsafe combinations.
3. Reconciler validation and status conditions.

Schema validation, admission webhooks, reconciler validation, and status conditions all express the same safety rules. This keeps installs defensible even when a webhook is temporarily unavailable.

## Supply Chain

Current release artifacts and checks include:

- minimal non-root images
- version-pinned and sha256-verified NUT source builds for NUT-bearing images
- OCI source, revision, version, license, documentation, creation time, and vendor labels
- image healthchecks for directly runnable operand and actuator images
- Kubernetes liveness/readiness probes for the manager
- SBOMs
- vulnerability scans
- keyless Sigstore/cosign signatures for published non-PR images
- private-address leak scanning
- the `main` tag applied only to a digest e2e and the NUT TLS smoke test have run against
- NUT source verified against a committed upstream signing key as well as sha256

The NUT source check is worth stating precisely, because the two halves answer different questions.
The sha256 answers "did the bytes arrive intact" and cannot answer "did upstream publish these
bytes", since it is a value this repository chose by looking at the same download. The detached
signature answers the second, but only because the key is not fetched alongside the tarball --
whoever served a bad tarball could serve a matching key. The key is committed at
`images/nut-signing-key.asc` and the build makes no keyserver call.

Local process tests are not sufficient evidence for deployment. Image-level smoke tests and
in-cluster validation are separate gates.
