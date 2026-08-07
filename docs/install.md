# Installation

How to install and run the operator in a cluster. For building from source, see the Development
section of the [README](../README.md).

The operator ships as a single bundled manifest, the standard install shape for a kubebuilder
operator. There is no Helm chart: the RBAC, CRDs, webhook configuration, and cert wiring are all
generated from `config/` by Kustomize, and a hand-maintained chart would be a second source of truth
for the same objects. If you need to customize the install, use the Kustomize path below.

## Prerequisites

**Kubernetes.** Verified against 1.34 in CI (envtest and kind). The manifests use
`apiextensions.k8s.io/v1`, `admissionregistration.k8s.io/v1`, `policy/v1`, and
`networking.k8s.io/v1`, so 1.21 is the structural floor. No CEL validation rules are used. Older
versions are untested.

**cert-manager — required.** The operator serves admission webhooks, and the bundled manifest
includes a `cert-manager.io/v1` `Certificate` and `Issuer` for the webhook serving cert. Without
cert-manager installed, that `Certificate` never issues, the webhook Secret is never created, and
the manager pod will not become ready.

```sh
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml
kubectl -n cert-manager rollout status deploy/cert-manager-webhook
```

### If cert-manager is not an option

cert-manager is the default because the bundled manifest wires it up, not because the operator
depends on it. The manager reads its serving cert from a directory
(`--webhook-cert-path=/tmp/k8s-webhook-server/serving-certs`, mounted from the `webhook-server-cert`
Secret), so **any issuer that can produce a Secret with `tls.crt` and `tls.key` works**.

To bring your own certificate:

1. Create a Secret named `webhook-server-cert` in the operator's namespace containing `tls.crt` and
   `tls.key`, with a SAN for `nut-operator-webhook-service.<namespace>.svc`.
2. Drop the `../certmanager` resource from your overlay.
3. Set `.webhooks[].clientConfig.caBundle` on both `MutatingWebhookConfiguration` and
   `ValidatingWebhookConfiguration` to your base64-encoded CA. This is the step cert-manager's
   ca-injector normally automates, and it must be repeated whenever the CA rotates.

On OpenShift, the built-in service CA does both halves with annotations and no extra operator:
`service.beta.openshift.io/serving-cert-secret-name: webhook-server-cert` on the webhook Service,
and `service.beta.openshift.io/inject-cabundle: "true"` on the two webhook configurations.

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
[audit-storage-schema.md](design/audit-storage-schema.md).

**Image pull access to `ghcr.io`.** Four images are needed — the operator plus three operands. See
[Images](#images) below.

## Install

### Bundled manifest

```sh
kubectl apply -f https://raw.githubusercontent.com/MichaelZalud18/nut-operator/main/dist/install.yaml
```

This creates the `nut-operator-system` namespace, all 10 CRDs, RBAC, the webhook configuration and
its cert-manager `Certificate`, a metrics `Service`, two `NetworkPolicy` objects, and the
controller-manager `Deployment` (1 replica, leader election enabled).

### Kustomize

Use this when you need a different namespace, a pinned image digest, or your own patches. Point a
kustomization at the repository's `config/default`:

```yaml
# kustomization.yaml
resources:
  - github.com/MichaelZalud18/nut-operator/config/default?ref=main
images:
  - name: controller
    newName: ghcr.io/michaelzalud18/nut-operator
    digest: sha256:<digest>
```

```sh
kubectl apply -k .
```

### Verify

```sh
kubectl -n nut-operator-system rollout status deploy/nut-operator-controller-manager
kubectl get crd | grep power.zalud.io          # expect 10
kubectl -n nut-operator-system get secret webhook-server-cert   # created by cert-manager
```

A manager pod that restarts roughly every two minutes with no obvious error is almost always the
webhook cert never being issued. Check that cert-manager is running.

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

## Configure

Apply resources in this order. Everything defaults to dry-run: nothing powers off a node until you
explicitly opt in.

**1. Capability profiles** (recommended). The bundled catalog describes UPS models the project has
verified:

```sh
kubectl apply -f https://raw.githubusercontent.com/MichaelZalud18/nut-operator/main/config/catalog/upscapabilityprofiles.yaml
```

From a clone, `make deploy-catalog` does the same thing.

If your UPS is not in the catalog, that is expected and handled — see
[the FAQ](design/faq.md#what-if-my-ups-does-not-have-a-packaged-capability-profile).

**2. `PowerManagementCluster`** — cluster-wide policy: operand namespace, storage backend, shutdown
tiers, and the operand images every other resource inherits. See
[config/samples/power_v1alpha1_powermanagementcluster.yaml](../config/samples/power_v1alpha1_powermanagementcluster.yaml).

**3. `UPSDevice`** — one per UPS, with its network endpoint and a credential Secret reference.
SNMPv3 credentials go in the Secret, not the spec.

**4. `NUTServer`** — renders `upsd` and the NUT drivers for a set of devices, selected by label.

**5. Topology** — `PowerInventoryNode` (requires `nodeName`) and `PowerInventoryEdge` (requires
`from`, `to`, `relation`). This is what tells the planner which UPS feeds which node. An invalid or
incomplete graph blocks `ShutdownFlow` acceptance, by design.

**6. `NodePowerAgent`** — the per-node DaemonSet (requires `nutServerRefs`). Defaults to
`MonitorOnly`.

**7. `ShutdownFlow`** — the plan itself (requires `triggers`). Defaults to `DryRun`, which compiles
and publishes the full plan, including the wave ordering and the reasoning, without touching a node.

A complete worked topology is in [docs/examples/orion-cluster/](examples/orion-cluster/README.md).
For testing without real hardware, [docs/examples/simulation/](examples/simulation/) drives scripted
`Online`/`OnBattery`/`LowBattery` transitions through a real NUT driver.

## Verify the configuration

```sh
kubectl get powermanagementcluster,upsdevice,nutserver,nodepoweragent,shutdownflow
kubectl describe shutdownflow <name>     # Accepted, Degraded, ExecutionReady conditions
```

The compiled plan, dependency graph, waves, and diagram exports are published in the `ShutdownFlow`
status. Read them before enabling enforcement — that is what dry-run is for.

Two refusals are intentional and worth recognizing:

- **`UnidentifiedUPSDevice`** — a device matched no product capability profile, so nothing has been
  verified about it. Dry-run still compiles the whole plan; enforcement refuses unless
  `spec.safety.allowUnidentifiedDevices: true` records the acceptance in Git.
- **`TriggerUnsupportedByAllDevices`** — a trigger references telemetry (such as battery runtime)
  that none of the targeted devices report, so the plan could never fire.

## Upgrade

Re-apply the bundled manifest, or re-apply your Kustomize overlay with a new digest. CRD changes so
far have been additive. Your own custom resources are never modified by an upgrade, and CRD-authored
capability profiles always outrank bundled ones.

## Uninstall

**Order matters.** `NUTServer` and `NodePowerAgent` carry finalizers
(`power.zalud.io/nutserver-cleanup`, `power.zalud.io/nodepoweragent-cleanup`) so that deletion emits
an auditable teardown Event. If the CRDs or the operator are removed first, nothing remains to clear
those finalizers and the objects hang in `Terminating`.

```sh
# 1. Your resources first, while the operator is still running.
kubectl delete shutdownflow --all
kubectl delete nodepoweragent --all
kubectl delete nutserver --all
kubectl delete upsdevice,powerinventoryedge,powerinventorynode,powerinfrastructure --all
kubectl delete powermanagementcluster --all

# 2. Then the operator.
kubectl delete -f https://raw.githubusercontent.com/MichaelZalud18/nut-operator/main/dist/install.yaml
```

Step 2 removes the CRDs, and with them any remaining custom resources. PostgreSQL audit data is not
touched — it outlives the operator on purpose.

If something is already stuck in `Terminating`, reinstall the operator and let it finish, or clear
the finalizer by hand:

```sh
kubectl patch nutserver <name> --type=merge -p '{"metadata":{"finalizers":[]}}'
```

## Troubleshooting

| Symptom | Cause |
| --- | --- |
| Manager pod restarts every ~2 minutes | cert-manager missing, so the webhook cert never issued. |
| `ImagePullBackOff` on the manager | Image tag unreachable, or GHCR credentials needed. |
| `NUTServer`/`NodePowerAgent` rejected for a missing image repository | No operand image set on the resource or on `PowerManagementCluster.spec.images`. |
| Prometheus scrapes time out (rather than refuse) | Scraping namespace is missing the `metrics: enabled` label. |
| `UPSDevice` telemetry stuck `Unavailable`/`Stale` | UPS unreachable on its endpoint, wrong driver, or wrong credentials. `kubectl describe upsdevice` carries the connection error. |
| `ShutdownFlow` not `Accepted` | Usually an incomplete inventory graph. The condition message names the specific diagnostic. |
| Storage not ready | Database unreachable, credentials wrong, or migrations failing. `PowerManagementCluster` status carries the reason. |
