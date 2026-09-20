# Managed NUT-only installation

Components: Modular Deployment Profiles; NUT Server / upsd.
Audience: operators.

Use this profile to manage NUT servers and consume UPS telemetry from an external system.
It runs the same manager binary with only NUTServer reconciliation and UPSDevice/NUTServer
admission. Each operand retains the separate upsd and driver-supervisor containers, generated
configuration, readiness, credential separation and lifecycle management.

There is no planner, executor, node agent, inventory controller or PostgreSQL dependency.
UPSDevice objects describe devices; their normalized status is not populated in this profile.
Read telemetry over the NUT protocol. Logs, events and NUTServer current status describe
operation; this profile offers no durable shutdown-execution history.

## Install the profile

Use a clean cluster or uninstall the full operator first. The profiles share resource names and
are alternatives, not co-installable managers. Applying the smaller bundle over a full install
does not remove old CRDs, RBAC or resources and is not a profile migration procedure.

From the checkout, generate the bundle with the manager and operand references you intend to run:

```sh
make build-installer-nut-only IMG='<manager-image-reference>' NUT_SERVER_IMG='<nut-server-image-reference>'
kubectl apply -f dist/install-nut-only.yaml
bash hack/webhook-cert.sh
kubectl rollout status deployment/nut-operator-controller-manager -n nut-operator-system
```

Pin images by digest for repeatable installation. The generator derives the two CRDs and four
admission entries from canonical generated manifests, restricts the manager role to NUT resources
and required Kubernetes operands, and sets `--profile=nut-only` and `--nut-server-image`.
It ships no cert-manager objects. The existing [webhook certificate procedure](webhook-certificate.md)
provides bootstrap and rotation; do not disable admission. Misspelled profiles, missing default
images and disabled admission fail startup. Individual NUTServers can override `spec.image`.

The manager still needs cluster-wide device/server discovery and permission to manage operand
namespaces, Secrets, configuration and Deployments. This is not a tenant isolation boundary.
It has no node, agent, flow, inventory or management-cluster permissions. Do not set
`managementClusterRef`; the profile rejects that dependency during reconciliation.

## Configure a server and client access

Create an operand namespace and supply a TLS Secret there. Its `tls.crt` must contain the server
certificate/chain and `tls.key` the matching PEM private key. Include the Service DNS names in
the certificate SANs, for example `telemetry-ups.power-telemetry.svc` and
`telemetry-ups.power-telemetry.svc.cluster.local`. Use your CA/issuer and distribute its trust
bundle to clients; no private keys are supplied by this repository.

```sh
kubectl create namespace power-telemetry
kubectl create secret tls nut-server-tls -n power-telemetry --cert=server-chain.pem --key=server-key.pem
```

This harmless dummy device is a starting example; replace it with your selected supported driver
and its configuration when connecting hardware:

```yaml
apiVersion: power.zalud.io/v1alpha1
kind: UPSDevice
metadata:
  name: example-ups
spec:
  driver: dummy-ups
  displayName: example-ups
---
apiVersion: power.zalud.io/v1alpha1
kind: NUTServer
metadata:
  name: telemetry-ups
spec:
  namespace: power-telemetry
  deviceRefs:
    - name: example-ups
  auth:
    mode: OperatorManaged
  tls:
    mode: Required
    serverCertificateRef:
      namespace: power-telemetry
      name: nut-server-tls
  clientAccess:
    - namespaceSelector:
        matchLabels:
          kubernetes.io/metadata.name: monitoring
      podSelector:
        matchLabels:
          app: nut-consumer
```

`clientAccess` adds TCP ingress to the NUT Service port. Within one entry, namespace and pod
selectors are ANDed; entries are ORed. An omitted pod selector allows all pods in the explicitly
selected namespaces. `{}` as a namespace selector explicitly selects all namespaces. Existing
same-namespace and manager access remains. Use a NetworkPolicy-enforcing CNI; selectors do not
authenticate users, and Service exposure does not override ingress policy.

The portable profile supports Kubernetes client selectors. For off-cluster consumers, provide
a separately reviewed network path/policy with your CNI and validate the source address after
Service routing. This example makes no blanket external-CIDR or LoadBalancer access promise.

## Telemetry, TLS and credentials

The Service is `telemetry-ups.power-telemetry.svc:3493`; the NUT UPS name defaults to the
UPSDevice name (`example-ups`). Use NUT `LIST UPS` / `GET VAR` / `LIST VAR` as appropriate.
Consume `ups.status`, battery/runtime values, device identity and supported variables through
your NUT client. Query availability is separate from NUTServer readiness and its upstream TCP
probe. Handle connection failure, `DRIVER-NOT-CONNECTED`, `DATA-STALE` and unknown UPS/variable
errors as unavailable telemetry; do not retain an old Online value as fresh indefinitely.

Configure clients to require STARTTLS and verify both CA trust and the Service hostname.
`tls.mode: Required` requires server certificate configuration and makes managed clients require
TLS; upsd's listener itself still supports plaintext NUT requests. In this profile external
clients own TLS enforcement. NUT variable reads are not protected by monitor passwords:
network access controls who can read telemetry. Monitor login and privileged NUT commands have
separate authentication/authorization. Mutual TLS remains unsupported by the shipped operand.

`OperatorManaged` creates `<server-name>-nut-users` in the operand namespace with separate
`admin-password`, `monitor-password` and `upsd.users` keys. Give consumers only the credential
they need through your Secret distribution mechanism; do not copy the whole administrator
Secret or log its contents. `ExistingSecret` accepts an operand-namespace Secret with an
`upsd.users` key and a matching `auth.existingSecretRef`.

Device spec, selected-device membership and referenced configuration changes reconcile through
the same renderer as a full install. Credential changes reach the projected user file and upsd
reload path. Certificate changes restart the operand to load new TLS material. Update client
trust before changing issuers. Driver recovery is isolated in the supervisor; it need not
restart upsd or recreate the pod.

For an appliance already serving NUT, see [upstream relay](../contributing/design/upstream-nut-relay.md).
The shipped repeater supports only `auth.mode: None`; authenticated or verified-TLS upstream
connections cannot be requested through this implementation. `Default` and `Secret` are rejected
instead of silently dropping their requested policy. The downstream Service still has its own
TLS and user configuration.

## Update, remove and validate

Regenerate with the intended image references, review the bundle, and apply it through your
normal workflow. Re-run `hack/webhook-cert.sh` when bootstrapping/rotating webhook certificates.
Reapplying the same profile and restarting its manager preserves declarative operands; this
does not claim compatibility with an arbitrary older schema or support an in-place switch
between full and NUT-only profiles.

Before uninstalling, remove the profile's NUTServers and UPSDevices, then delete the generated
installer. Review externally supplied credential/certificate Secrets and operand namespaces
separately; do not assume uninstall removes user-owned material.

The reproducible clean-profile acceptance uses an owned disposable Kind cluster:

```sh
CERT_MANAGER_INSTALL_SKIP=true python3 -B hack/test-kind.py --focus 'NUT-only'
```

It exercises real dummy-ups telemetry, TLS/auth, allowed/denied clients, updates, rotations,
driver recovery, relay behavior, reapplication and omitted APIs/permissions. Current results
and remaining work belong to the [modular task tracker](../tasks.md#modular-deployment-profiles).
