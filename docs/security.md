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

Approved `SystemdPoweroff` rendering uses `hostPID` and adds only `CAP_SYS_BOOT` to the actuator
container. It remains non-root, drops all other capabilities, keeps privilege escalation disabled,
uses a read-only root filesystem, and receives no Kubernetes service-account token. The container
seccomp profile is unconfined for this mode because common runtime-default profiles block the Linux
`reboot(2)` syscall used for host poweroff.

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

Release artifacts include:

- minimal non-root images
- pinned base images
- image healthchecks and Kubernetes liveness/readiness probes
- SBOMs
- vulnerability scans
- signed images
- immutable digest references in production manifests

Local process tests are not sufficient evidence for deployment. Image-level smoke tests and in-cluster validation are separate gates.
