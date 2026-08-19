# Choosing a webhook certificate path

Components: Cross-cutting.
Audience: operators.

The operator serves admission webhooks, and admission is load-bearing for safety here rather than
cosmetic. You have to supply a serving certificate before the manager will start, and which way you
supply it has consequences during an outage. That makes it the one real decision in installing this
operator, which is why it has its own page.

The reasoning behind the recommendation — the trust model, the CA-key exposure, why in-process
generation was declined — is in
[Security](../reference/security.md#admission-webhook-certificate). This page is what to do.

**Pre-v1: there is no tagged release.** Every `nut-operator` URL below points at `main`, and moves
when `main` moves. Release-asset URLs will exist at v1; until then, pin by cloning at a commit if you
need a fixed target. The cert-manager URL is upstream's own release and is stable.

**Both bundles have the same trust model** — a privately issued CA is the correct shape for an
admission webhook, not a compromise. What differs is what has to be working for admission to work.

| | `dist/install-byo-cert.yaml` (recommended) | `dist/install.yaml` |
| --- | --- | --- |
| Cluster dependency | None | cert-manager (its CRDs and Deployments) |
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

### Path A — no cert-manager (recommended)

From a clone, which is the simplest form and the one to prefer pre-v1:

```sh
git clone https://github.com/MichaelZalud18/nut-operator.git
cd nut-operator
kubectl apply -f dist/install-byo-cert.yaml
./hack/webhook-cert.sh
```

`make deploy-byo-cert` does both steps in one, building the overlay from `config/byo-cert` rather
than the committed bundle.

Without a clone. The script needs only `kubectl` and `openssl` — it reads nothing else from the
repository — so it runs on its own:

```sh
kubectl apply -f https://raw.githubusercontent.com/MichaelZalud18/nut-operator/main/dist/install-byo-cert.yaml
curl -fsSLO https://raw.githubusercontent.com/MichaelZalud18/nut-operator/main/hack/webhook-cert.sh
chmod +x webhook-cert.sh
./webhook-cert.sh
```

Downloaded rather than piped into a shell on purpose: this script mints the CA the webhook trusts,
which is worth reading before running.

Either way, the manager pod stays in `ContainerCreating` until the script runs — the
`webhook-server-cert` Secret is a non-optional volume — and becomes ready immediately after.

The script mints a long-lived CA (10 years, stored in the `nut-operator-webhook-ca` Secret) and a
serving certificate from it (1 year), writes `webhook-server-cert`, and patches `caBundle` into both
webhook configurations. It is idempotent and it is also the rotation procedure: re-run it. The CA is
reused, so `caBundle` stays valid and only the serving certificate changes; the manager picks that up
live through its controller-runtime `certwatcher`, with no restart. `--help` lists the flags,
including `--ca-cert`/`--ca-key` to sign from an existing CA without storing its key in the cluster.

Because nothing renews the certificate on its own here, the manager publishes when it expires:
`nutoperator_certificate_not_after_timestamp_seconds{certificate="webhook"}`. Alert on it with
whatever lead time suits your rotation procedure — see [Metrics](../reference/metrics.md).

The generated CA key lives in a cluster Secret. Use `--ca-cert`/`--ca-key` to sign from an existing
CA if you would rather it never did — see
[the exposure model](../reference/security.md#admission-webhook-certificate).

### Path B — cert-manager

```sh
kubectl apply -f https://github.com/cert-manager/cert-manager/releases/latest/download/cert-manager.yaml
kubectl -n cert-manager rollout status deploy/cert-manager-webhook
kubectl apply -f https://raw.githubusercontent.com/MichaelZalud18/nut-operator/main/dist/install.yaml
```

Pick this if you already run cert-manager and want the renewal automated. Applying `install.yaml`
without cert-manager present fails outright, because its `Certificate` and `Issuer` CRDs do not
exist. If you go this route, pin cert-manager to a tier that outlives the operator so a shutdown does
not take it down first.

### Any other issuer

The manager reads its serving cert from a directory
(`--webhook-cert-path=/tmp/k8s-webhook-server/serving-certs`, mounted from the `webhook-server-cert`
Secret), so **any issuer that can produce a Secret with `tls.crt` and `tls.key` works**. Start from
the `config/byo-cert` overlay, which already removes all cert-manager wiring, supply the Secret
yourself, and set `.webhooks[].clientConfig.caBundle` on both webhook configurations to your
base64-encoded CA.

On OpenShift the built-in service CA does both halves with annotations and no extra operator:
`service.beta.openshift.io/serving-cert-secret-name: webhook-server-cert` on the webhook Service, and
`service.beta.openshift.io/inject-cabundle: "true"` on the two webhook configurations.

**Do not simply remove the webhooks.** Admission *defaulting* is load-bearing for safety and is not
duplicated anywhere else — without it the tier-0 DaemonSet pods land in BestEffort QoS with no
OOM-score protection. Generating the certificate in-process, the third option you may have
seen in other operators, was also considered and declined.

## Next

Back to [Installation](README.md#install) to apply the bundle.
