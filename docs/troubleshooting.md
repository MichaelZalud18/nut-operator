# Troubleshooting

Components: Cross-cutting.
Audience: operators.

Symptoms and their causes. For a node that stayed up after a shutdown signal, the actuator names the
link that broke on its own — see
[Enabling actuation](guides/enable-actuation.md#reading-the-gate-trace).

## Install and startup

| Symptom | Cause |
| --- | --- |
| `kubectl apply` of `install.yaml` fails on `Certificate`/`Issuer` | cert-manager is not installed, so its CRDs do not exist. Use `install-byo-cert.yaml` instead. |
| Manager pod stuck in `ContainerCreating` with `FailedMount` on `webhook-certs` | The `webhook-server-cert` Secret does not exist. Run `hack/webhook-cert.sh`, or check that cert-manager issued it. |
| API rejects CR creates with `failed calling webhook ... x509` | `caBundle` on the webhook configurations does not match the serving certificate's CA. Re-run `hack/webhook-cert.sh`. |
| Manager `CrashLoopBackOff` with `controller with name ... already exists` | A reconciler is registered twice in `cmd/main.go` (F-38). Guarded by `TestMainRegistersEachReconcilerOnce`. |
| `ImagePullBackOff` on the manager | Image tag unreachable, or GHCR credentials needed. |

## Configuration

| Symptom | Cause |
| --- | --- |
| `NUTServer`/`NodePowerAgent` rejected for a missing image repository | No operand image set on the resource or on `PowerManagementCluster.spec.images`. |
| Prometheus scrapes time out (rather than refuse) | Scraping namespace is missing the `metrics: enabled` label. |

## Runtime

| Symptom | Cause |
| --- | --- |
| `UPSDevice` telemetry stuck `Unavailable`/`Stale` | UPS unreachable on its endpoint, wrong driver, or wrong credentials. `kubectl describe upsdevice` carries the connection error. |
| `ShutdownFlow` not `Accepted` | Usually an incomplete inventory graph. The condition message names the specific diagnostic. |
| Storage not ready | Database unreachable, credentials wrong, or migrations failing. `PowerManagementCluster` status carries the reason. |
