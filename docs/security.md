# Security

`nut-operator` manages infrastructure that can shut down physical nodes. Security is part of the product contract, not a deployment afterthought.

## Defaults

- Real actuation is off by default.
- `NodePowerAgent` starts in `DryRun`.
- `ShutdownFlow` starts in `DryRun`.
- Real host shutdown requires an approval annotation on the exact resource that requests enforcement.
- Durable state defaults to PostgreSQL/CNPG, not local files.
- Non-actuator containers should run as non-root with read-only root filesystems and `RuntimeDefault` seccomp.
- UPS connectivity is network-only by default. Local USB and serial UPS access are unsupported, so NUT server and monitor containers do not require host device mounts or privileged mode.
- UPS drivers are accepted through an explicit network-driver allowlist. Unknown drivers are rejected until reviewed.
- Appliances that expose their own NUT server use explicit upstream relay configuration and still render as non-privileged network-only pods.

## Privilege Boundary

The host-action boundary is intentionally narrow.

The NUT server and client containers use network UPS protocols only. They should not need broad Linux capabilities, host devices, host namespaces, or Kubernetes API tokens.

The actuator container owns host interaction only when approved actuation is enabled. It should have no NUT credentials, no flow logic, and no broad policy authority. Its job is to validate the signal and execute the approved local action.

## Network Controls

Generated operands should be compatible with default-deny namespaces.

Expected policy edges:

- agent to `NUTServer` on TCP 3493
- operator to Kubernetes API
- operator and audit writer to PostgreSQL
- Prometheus to metrics endpoints when enabled
- DNS only where needed

`NUTServer` should not be externally exposed by default.

## Credential Controls

- NUT server users are operator-managed by default.
- Per-node secondary users are preferred over a shared fleet credential.
- Existing Secrets are supported for organizations with external secret management.
- SNMP credentials and PostgreSQL DSNs must always come from Secrets.
- Upstream NUT relay credentials use Secret-projected `nutauth.conf` files; unauthenticated appliances must explicitly use `auth.mode: None`.
- Generated credentials must be rotatable without recreating API objects.

## TLS

NUT protocol TLS defaults to `Required` in the API contract. Production renderers should require mounted certificate material before rendering required TLS mode as ready.

PostgreSQL TLS is required for external PostgreSQL by default.

## Policy Gates

Unsafe behavior should be blocked in three places:

1. CRD schema where possible.
2. Admission webhooks for object-local unsafe combinations.
3. Reconciler validation and status conditions.

The current implementation provides schema and reconciler validation across the API surface, plus admission webhooks for all v1alpha1 resources. Production-grade enforcement still requires broader in-cluster soak testing and release-signing gates.

## Supply Chain

Release artifacts should include:

- minimal non-root images
- pinned base images
- SBOMs
- vulnerability scans
- signed images
- immutable digest references in production manifests

Local process tests are not sufficient evidence for deployment. Image-level smoke tests and in-cluster validation are separate gates.
