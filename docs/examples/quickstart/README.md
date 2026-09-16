# Two-UPS Quickstart

Components: Cross-cutting.
Audience: operators.

Compile a reviewable shutdown plan with two simulated UPS devices, three Kubernetes Nodes and
one modeled switch. Everything stays `DryRun` / `Simulate`; no approval annotations are supplied.
Use a disposable three-node cluster (one control plane, two workers), not a production cluster.
The UPS wiring below is fictional; the Node names must be real members of that test cluster.

## Bootstrap

From the repository root, [install the operator and webhook certificate](../../installation/README.md).
Wait for the manager before creating custom resources:

```sh
kubectl apply -f dist/install-byo-cert.yaml
./hack/webhook-cert.sh
kubectl -n nut-operator-system rollout status deployment/nut-operator-controller-manager --timeout=5m
kubectl apply -f docs/examples/quickstart/bootstrap.yaml
kubectl get nodes
```

The bootstrap supplies operand images and the dedicated `quickstart-power` namespace.
`storage.mode: Disabled` is evaluation-only: no durable audit history. Production requires
PostgreSQL. Images follow the pre-v1 `main` tags, as in the existing simulation examples.

## 1. UPS devices and NUT

```sh
kubectl apply -f docs/examples/quickstart/ups-nut.yaml
kubectl get upsdevice quickstart-ups-a quickstart-ups-b
kubectl describe nutserver quickstart-nut
```

Two `UPSDevice` resources use the supported `dummy-ups` driver and a shared ConfigMap sequence.
Both match **one** capability profile by declared model; it declares exactly the fixture's readings,
including runtime for `RuntimeBelow`. The sequence alternates five minutes online and five minutes
on battery. Polling and thresholds belong to each device. One `NUTServer` selects both UPS devices
and renders one pod with `upsd` plus supervised drivers, not one server per UPS.

The simulated devices need no hardware credentials. NUT client passwords are operator-generated
Secrets, never literal example passwords. NUT protocol TLS is explicitly disabled for this trusted,
disposable evaluation; the webhook still uses TLS. For real hardware, use device credential Secret
references and NUT certificate/CA references described in
[configuration](../../installation/configuration.md#secrets-and-profiles), for example:

```yaml
# UPSDevice.spec (with a supported real driver, replacing simulation):
credentialSecretRef: {namespace: power-system, name: ups-device-credentials}
# NUTServer.spec:
tls:
  mode: Required
  serverCertificateRef: {namespace: power-system, name: nut-server-tls}
  serverCARef: {namespace: power-system, name: nut-server-ca}
```

Provision those Secrets separately; use the operand namespace you configured. Device credentials,
NUT client credentials and TLS material have different purposes. See the existing
[device sample](../../../config/samples/power_v1alpha1_upsdevice.yaml) and
[credential contract](../../reference/api.md) for driver-specific configuration.

## 2. Topology

Choose the three names from `kubectl get nodes`. Replace the values below, then verify them before
labeling or rendering. Python 3 with PyYAML is needed for the renderer and schema checker.
Install the renderer dependency with
`python3 -m pip install -r docs/examples/quickstart/requirements.txt` in your Python environment;
both test workflows use this same pinned dependency file.

```sh
CONTROL_NODE=your-test-control-node
WORKER_A=your-test-worker-a
WORKER_B=your-test-worker-b
kubectl get node "$CONTROL_NODE" "$WORKER_A" "$WORKER_B"
kubectl label node "$CONTROL_NODE" power.example.com/quickstart-role=control
kubectl label node "$WORKER_A" "$WORKER_B" power.example.com/quickstart-role=worker
TOPOLOGY=$(mktemp)
python3 docs/examples/quickstart/render.py --control "$CONTROL_NODE" \
  --worker-a "$WORKER_A" --worker-b "$WORKER_B" > "$TOPOLOGY"
kubectl apply -f "$TOPOLOGY"
```

Do not apply the unrendered `topology.yaml`. `PowerInventoryNode.spec.nodeName` and `Node` edge
endpoints must bind actual Kubernetes Nodes; external machines cannot be invented there.

| Power domain | Derived from `Feeds` edges |
| --- | --- |
| `quickstart-a` | UPS A supplies the control node, worker A and switch |
| `quickstart-b` | UPS B supplies worker B |

`PowerInfrastructure` models the switch. Each `Feeds` edge names a supply input; `Carries` edges
connect the switch to all three Nodes. Worker B has independent power but still depends on the
switch on UPS A for communication. Topology records dependencies; it does not authorize shutting
down a switch, router, PDU or external server. Broader layouts use these same resource kinds and
relationships; see [modeling topology](../../guides/model-your-topology.md).

## 3. Shutdown flow

```sh
kubectl apply -f docs/examples/quickstart/shutdown.yaml
kubectl get nodepoweragent quickstart-workers quickstart-control
kubectl wait shutdownflow/quickstart --for=condition=Accepted --timeout=2m
kubectl get shutdownflow quickstart -o jsonpath='{range .status.compiledWaves[*]}{.index}{"\t"}{.shutdownTier}{"\t"}{.duration}{"\t"}{.groups}{"\n"}{end}'
kubectl get shutdownflow quickstart -o yaml
```

Two node-agent resources provide delivery to the worker and control roles; each renders a
DaemonSet, not one CR per Node. Both inherit images, monitor the shared NUTServer, and remain
`DryRun` / `Simulate`. Check their `status.selectedNodes` covers exactly two workers and one control
node. They are prerequisites for node shutdown, not a fourth configuration domain.

The flow owns triggers, groups, actions and ordering. It declares both shared API/NUT paths through
the switch. Higher tiers stop earlier; the explicit `before` edge ensures draining completes before
shutdown within tier 2. Before a trigger selects a narrower execution scope, expect:

```text
0   2   2m0s   ["drain-workers"]
1   2   1m0s   ["stop-workers"]
2   1   1m0s   ["stop-control"]
```

Review `status.compiledWaves`, `status.compiledSteps`, `status.publishedArtifact`, and
`status.compileDiagnostics`; the initial estimate is four minutes. `Accepted=True` establishes
compilation, not operand health or permission to actuate. Check device telemetry/profile conditions,
server readiness, and agent coverage too. On-battery eligibility arrives after the fixture changes
state and the hold interval passes; DryRun does not drain or halt Nodes.

Real actuation is a separate workflow: [enable actuation](../../guides/enable-actuation.md) only
after replacing simulated power/wiring with verified inputs and reviewing the resulting plan.
For external actions, configure a `ShutdownHook` separately; advisory delivery is not proof that
an external target stopped.

## Grow the setup

- **UPS/NUT:** add devices to the server selection, choose matching profiles, and supply credential
  and TLS Secret references. Several UPS devices may share a profile or a server.
- **Topology:** add inventory bindings and complete supply/communication edges when wiring changes.
  Do not maintain a second list of derived domain membership.
- **Shutdown flow:** adjust triggers, groups, tiers, dependencies and hooks when shutdown policy
  changes. Recompile and review before enabling enforcement.

## Validate and clean up

```sh
python3 hack/validate-samples.py
go test ./test/quickstart -v
```

Schema validation uses the checked-in CRDs (`make validate-samples` regenerates them first).
The component test reads these files, invokes the renderer, resolves topology/profiles and compiles
the plan through the production adapters. Nodes and agent coverage are synthetic; it does not
prove API admission, image availability, NUT polling or DaemonSet readiness.

`make test-e2e` includes the **Two-UPS quickstart install-to-plan** spec. The existing harness creates
and removes its own three-node Kind cluster and private kubeconfig; it never uses an existing
cluster. The spec applies the BYO-certificate installer and these resources, binds discovered Nodes,
and checks telemetry, agent coverage and published waves. It substitutes locally loaded images.
The host guard requires at least 512 inotify instances; a host at 128 cannot run this validation.
Do not lower the guard or use another cluster to claim a pass. A component pass leaves this
install-to-plan gate unverified until it runs on a suitable disposable host.

Remove only this example while the operator is still installed:

```sh
kubectl delete -f docs/examples/quickstart/shutdown.yaml
kubectl delete -f "$TOPOLOGY"
kubectl delete -f docs/examples/quickstart/ups-nut.yaml
kubectl delete -f docs/examples/quickstart/bootstrap.yaml
kubectl label node "$CONTROL_NODE" "$WORKER_A" "$WORKER_B" power.example.com/quickstart-role-
rm "$TOPOLOGY"
```

Then dispose of the test cluster using its owning tool, or follow the
[operator uninstall order](../../installation/upgrade-and-uninstall.md).
