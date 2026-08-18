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
load-bearing for safety here. There are two supported ways to provide one, and which you pick has
consequences during an outage — that is the [next section](#choosing-a-certificate-path), and the
only real decision in this page.

**PostgreSQL — required for production, optional for evaluation.** Kubernetes holds desired state;
PostgreSQL holds the record of what actually happened. Which backend, and the tradeoff between them,
is in [the API reference](../reference/api.md#storage). What matters before you install:

- `CNPG` is the default and needs the [CNPG operator](https://cloudnative-pg.io/) installed first.
- `ExternalPostgres` needs a reachable database and a DSN in a Secret.
- `Disabled` needs nothing, keeps no audit trail, and is for evaluation only.

**Image pull access to `ghcr.io`.** The operator image plus its operand images. See
[Images](#images) below.

## Choosing a certificate path

The manager will not start without a webhook serving certificate, and the two supported ways to
supply one differ in what has to be working for admission to work during an outage. The recommended
path has no cert-manager dependency.

**[Choose a certificate path](webhook-certificate.md)** — the tradeoff, both bundles, any other
issuer, and the rotation procedure.

## Install

### Bundled manifest

```sh
# Recommended: no cert-manager dependency.
kubectl apply -f https://raw.githubusercontent.com/MichaelZalud18/nut-operator/main/dist/install-byo-cert.yaml
./hack/webhook-cert.sh

# Or, if you already run cert-manager:
kubectl apply -f https://raw.githubusercontent.com/MichaelZalud18/nut-operator/main/dist/install.yaml
```

Either creates the `nut-operator-system` namespace, every CRD, RBAC, the webhook configuration, a
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
kubectl get crd | grep power.zalud.io          # every power.zalud.io CRD
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

**The operand images have no built-in defaults.** `NUTServer` and `NodePowerAgent` fail to
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
