# Installation

Components: Cross-cutting.

How to install and run the operator in a cluster. For building from source, see the Development
section of the [README](../../README.md).

The operator ships as a single bundled manifest, the standard install shape for a kubebuilder
operator. There is no Helm chart: the RBAC, CRDs, webhook configuration, and cert wiring are all
generated from `config/` by Kustomize, and a hand-maintained chart would be a second source of truth
for the same objects. If you need to customize the install, use the Kustomize path below.

## Prerequisites

**Kubernetes.** Verified against 1.34 in CI (envtest and kind). The manifests use
`apiextensions.k8s.io/v1`, `admissionregistration.k8s.io/v1`, `policy/v1`, and
`networking.k8s.io/v1`, so 1.21 is the minimum supported version. No CEL validation rules are used. Older
versions are untested.

**A webhook serving certificate.** The operator serves admission webhooks, and admission is
load-bearing for safety here (see below). There are two supported ways to provide the certificate,
and which one you pick has consequences during an outage.

### Choosing a certificate path

Admission webhook certificates are not validated against any public or cluster trust store. The API
server validates the webhook's certificate against `.webhooks[].clientConfig.caBundle` on the webhook
configuration object, which only a cluster administrator can write. A privately issued CA is
therefore the correct shape here, not a compromise — the bundled `config/certmanager/issuer.yaml` is
itself a `selfSigned: {}` `Issuer`. **Both paths below have the same trust model.** What differs is
what has to be working for admission to work.

| | `dist/install-byo-cert.yaml` (recommended) | `dist/install.yaml` |
| --- | --- | --- |
| Cluster dependency | None | cert-manager (CRDs + 3 Deployments) |
| Serving certificate | Static Secret, minted by `hack/webhook-cert.sh` | Issued by cert-manager |
| `caBundle` injection | The same script, at provisioning time | cert-manager ca-injector, continuously |
| Renewal | Re-run the script | Automatic |

**The recommendation is the bring-your-own path, and the reason is this operator's job.** It has to
work while the cluster is losing power, and during a tiered shutdown the workloads it depends on are
themselves being stopped — cert-manager among them, if it is not pinned to a late tier. A serving
certificate that is only a static Secret has nothing to reconcile, nothing to issue, and nothing to
inject: the kubelet mounts it and the manager serves. That property is worth more here than automatic
renewal is.

Keep this in proportion. No webhook rule matches the `/status` subresource, so a broken or missing
webhook certificate does not stop an in-flight `ShutdownFlow` from recording progress. What it blocks
is changes to resource *specs* — a human overriding a flow or adjusting a tier mid-event. That is the
intervention path, not the execution path, and it is exactly the path you want available during an
incident.

The cost of the recommended path is that nobody rotates the certificate for you. That is a
normal-operations discipline problem, not an outage problem, which is why it does not change the
recommendation.

#### Path A — no cert-manager (recommended)

```sh
kubectl apply -f https://github.com/MichaelZalud18/nut-operator/releases/latest/download/install-byo-cert.yaml
./hack/webhook-cert.sh
```

The manager pod stays in `ContainerCreating` until the script runs — the `webhook-server-cert` Secret
is a non-optional volume — and becomes ready immediately after. From a clone, `make deploy-byo-cert`
does both steps.

The script mints a long-lived CA (10 years, stored in the `nut-operator-webhook-ca` Secret) and a
serving certificate from it (1 year), writes `webhook-server-cert`, and patches `caBundle` into both
webhook configurations. It is idempotent and it is also the rotation procedure: re-run it. The CA is
reused, so `caBundle` stays valid and only the serving certificate changes; the manager picks that up
live through its controller-runtime `certwatcher`, with no restart. `--help` lists the flags,
including `--ca-cert`/`--ca-key` to sign from an existing CA without storing its key in the cluster.

Because nothing renews the certificate on its own here, the manager publishes when it expires:
`nutoperator_certificate_not_after_timestamp_seconds{certificate="webhook"}`. Alert on it with
whatever lead time suits your rotation procedure — see [metrics.md](../reference/metrics.md).

Storing the generated CA key in a cluster Secret is the same exposure model cert-manager and
`cert-controller` both have: read access to that Secret is enough to mint a certificate this
webhook's `caBundle` trusts. Use `--ca-cert`/`--ca-key` if you would rather that key never live in
the cluster.

#### Path B — cert-manager

```sh
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml
kubectl -n cert-manager rollout status deploy/cert-manager-webhook
kubectl apply -f https://github.com/MichaelZalud18/nut-operator/releases/latest/download/install.yaml
```

Pick this if you already run cert-manager and want the renewal automated. Applying `install.yaml`
without cert-manager present fails outright, because its `Certificate` and `Issuer` CRDs do not
exist. If you go this route, pin cert-manager to a tier that outlives the operator so a shutdown does
not take it down first.

#### Any other issuer

The manager reads its serving cert from a directory
(`--webhook-cert-path=/tmp/k8s-webhook-server/serving-certs`, mounted from the `webhook-server-cert`
Secret), so **any issuer that can produce a Secret with `tls.crt` and `tls.key` works**. Start from
the `config/byo-cert` overlay, which already removes all cert-manager wiring, supply the Secret
yourself, and set `.webhooks[].clientConfig.caBundle` on both webhook configurations to your
base64-encoded CA.

On OpenShift the built-in service CA does both halves with annotations and no extra operator:
`service.beta.openshift.io/serving-cert-secret-name: webhook-server-cert` on the webhook Service, and
`service.beta.openshift.io/inject-cabundle: "true"` on the two webhook configurations.

#### In-process certificate rotation

Some operators generate and rotate their own serving certificate in-process, most commonly with
[`open-policy-agent/cert-controller`](https://github.com/open-policy-agent/cert-controller) — the
library Gatekeeper, Kueue, JobSet, LeaderWorkerSet, MetalLB, KEDA, Azure Workload Identity, and the
Kubeflow operators all use for exactly this. **This operator does not do that, deliberately.**

It is a legitimate, widely deployed option; the reasons it is not the default here are specific to
this operator rather than objections to the library:

- **The certificate is generated after the pod starts.** That is a window (Kueue documents 10–30
  seconds) in which the webhook is not yet serving HTTPS. Every webhook here is
  `failurePolicy: Fail`, so during it, creates and updates of this operator's custom resources are
  rejected — and a manager pod rescheduled mid-outage hits that window at the worst possible time.
  With a static Secret the pod does not start at all until a valid certificate exists: the failure
  is loud and up front rather than a brief, quiet rejection window.
- **It requires granting the operator cluster-scoped write on admission configuration.** Injecting
  `caBundle` in-process means `update` on `validatingwebhookconfigurations` and
  `mutatingwebhookconfigurations`. That can be narrowed with `resourceNames` to this operator's own
  two objects, but it is still a privilege the operator does not hold today, and admission
  configuration is a high-value target.
- **The trust model is identical.** In-process generation would not weaken anything — but it would
  not strengthen anything either, so a removed dependency is the whole of what is being bought.

Path A removes the same install-time dependency without the startup window or the extra RBAC, which
is why it is the recommendation rather than this.

**Do not simply remove the webhooks.** Admission defaulting is load-bearing for safety: the
`NodePowerAgent` defaulter is the only thing that sets `spec.resources.upsmon` and
`spec.resources.actuator`, and without it the tier-0 DaemonSet pods land in BestEffort QoS with no
OOM-score protection or scheduler reservation (F-34). It also supplies
`spec.placement.priorityClassName`, which has no CRD-level default. Validation is duplicated in the
controllers as a second layer, but defaulting is not.

**PostgreSQL — required for production, optional for evaluation.** Kubernetes holds desired state;
PostgreSQL holds the record of what actually happened. Three modes:

| `spec.storage.mode` | Use |
| --- | --- |
| `CNPG` (default) | CloudNativePG `Cluster` in the same Kubernetes cluster. Install the [CNPG operator](https://cloudnative-pg.io/) first. |
| `ExternalPostgres` | A database outside the cluster, referenced by a DSN in a Secret. TLS required by default. |
| `Disabled` | Evaluation and development only. No audit trail is kept. |

`ExternalPostgres` is the more resilient choice for this workload: a database outside the cluster is
not in the shutdown path of the event it is recording. See
[audit-storage-schema.md](../contributing/design/audit-storage-schema.md).

**Image pull access to `ghcr.io`.** Four images are needed — the operator plus three operands. See
[Images](#images) below.

## Install

### Bundled manifest

```sh
# Recommended: no cert-manager dependency.
kubectl apply -f https://raw.githubusercontent.com/MichaelZalud18/nut-operator/main/dist/install-byo-cert.yaml
./hack/webhook-cert.sh

# Or, if you already run cert-manager:
kubectl apply -f https://raw.githubusercontent.com/MichaelZalud18/nut-operator/main/dist/install.yaml
```

Either creates the `nut-operator-system` namespace, all 10 CRDs, RBAC, the webhook configuration, a
metrics `Service`, two `NetworkPolicy` objects, and the controller-manager `Deployment` (1 replica,
leader election enabled). `install.yaml` additionally creates a cert-manager `Issuer` and
`Certificate`; `install-byo-cert.yaml` contains no cert-manager objects at all.

### Kustomize

Use this when you need a different namespace, a pinned image digest, or your own patches. Point a
kustomization at `config/byo-cert` (no cert-manager) or `config/default` (cert-manager):

```yaml
# kustomization.yaml
resources:
  - github.com/MichaelZalud18/nut-operator/config/byo-cert?ref=main
images:
  - name: controller
    newName: ghcr.io/michaelzalud18/nut-operator
    digest: sha256:<digest>
```

```sh
kubectl apply -k .
./hack/webhook-cert.sh    # config/byo-cert only
```

### Verify

```sh
kubectl -n nut-operator-system get secret webhook-server-cert
kubectl -n nut-operator-system rollout status deploy/nut-operator-controller-manager
kubectl get crd | grep power.zalud.io          # expect 10
```

A manager pod sitting in `ContainerCreating` is almost always the `webhook-server-cert` Secret not
existing yet: run `hack/webhook-cert.sh`, or check that cert-manager issued it.

Confirm admission is actually reachable — this is the part a bad `caBundle` breaks, and it fails
silently until someone writes a resource:

```sh
kubectl apply --dry-run=server -f - <<'EOF'
apiVersion: power.zalud.io/v1alpha1
kind: NUTServer
metadata:
  name: webhook-probe
spec:
  namespace: power-system
  tls:
    mode: Required
EOF
```

Expect a rejection naming `spec.deviceRefs` and `spec.tls.serverCertificateRef`. Any `x509` or
`failed calling webhook` error instead means the certificate or `caBundle` is wrong.

## Images

| Image | Role |
| --- | --- |
| `ghcr.io/michaelzalud18/nut-operator` | Controller manager. Set by the install manifest. |
| `ghcr.io/michaelzalud18/nut-server` | `upsd` plus NUT drivers. Rendered by `NUTServer`. |
| `ghcr.io/michaelzalud18/upsmon-agent` | Unprivileged NUT client on each node. Rendered by `NodePowerAgent`. |
| `ghcr.io/michaelzalud18/node-actuator` | Minimal host-poweroff binary. Rendered by `NodePowerAgent`. |

`:main` tracks the default branch; `:sha-<git-sha>` tags are immutable. Production deployments
should pin digests.

**The three operand images have no built-in defaults.** `NUTServer` and `NodePowerAgent` fail to
render with a clear error unless an image repository is set, either per-resource on
`spec.image`/`spec.images`, or centrally on `PowerManagementCluster.spec.images`. Setting them once
on the cluster resource is the recommended shape — see the walkthrough below.

## Network requirements

Most traffic is inside the cluster. The one edge that usually needs a firewall rule is the operator
reaching the UPS itself.

| From | To | Port | Notes |
| --- | --- | --- | --- |
| `nut-server` pod | UPS device | UDP 161 | SNMP. The common case, and the rule that crosses a physical network boundary. Driver-dependent. |
| `nut-server` pod | Upstream NUT appliance | TCP 3493 | Only when using `UPSDevice.spec.upstreamNUT` relay mode. |
| Operator manager | `upsd` Service | TCP 3493 | Telemetry polling. Cross-namespace; allowed by the `NUTServer` NetworkPolicy. |
| `upsmon-agent` (each node) | `upsd` Service | TCP 3493 | Monitoring. |
| Operator manager | kube-apiserver | TCP 443 | |
| Operator manager | PostgreSQL | TCP 5432 | Audit writes. Not on the decision path. |
| kube-apiserver | Webhook Service | TCP 443 → 9443 | Admission validation. |
| Prometheus | Metrics endpoint | TCP 8443 | HTTPS, behind authn/authz. See the NetworkPolicy note below. |
| kubelet | Manager pod | TCP 8081 | Health and readiness probes. |

The bundled `allow-metrics-traffic` NetworkPolicy admits the metrics port **only from namespaces
labeled `metrics: enabled`**. If Prometheus scrapes are timing out rather than being refused, label
its namespace:

```sh
kubectl label ns monitoring metrics=enabled
```

`NUTServer` is not exposed outside the cluster by default.

## Next

- [Configuration](configuration.md) — the resources to apply, in order, and how to read the
  compiled plan.
- [Upgrade and uninstall](upgrade-and-uninstall.md) — and why deletion order matters.
- [Troubleshooting](../troubleshooting.md) — symptoms and causes.
