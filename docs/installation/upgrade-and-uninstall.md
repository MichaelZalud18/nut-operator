# Upgrade and uninstall

Components: Cross-cutting.
Audience: operators.

## Upgrade

Re-apply the bundled manifest, or re-apply your Kustomize overlay with a new digest. Pre-v1 changes
can tighten validation as well as add fields. Your own custom resources are never automatically
migrated by an upgrade, except for the obsolete finalizer metadata described below.
CRD-authored capability profiles always outrank bundled ones.

### Obsolete operand finalizers

The manager removes `power.zalud.io/nutserver-cleanup` and
`power.zalud.io/nodepoweragent-cleanup` during reconciliation, including from objects already
terminating. These keys previously delayed deletion only to emit an Event; they protected no
external cleanup. New resources receive neither key. Migration preserves other finalizers and
uses resource-version checks so concurrent updates are retried rather than overwritten.

Let the upgraded manager reconcile existing resources before removing it. A legacy object that
has not yet been reconciled still requires the manager to remove its old key. Invalid resources
may retire the exact legacy key through admission without changing spec, status, approvals, or
other finalizers. Normal updates still undergo full validation.

### Failure-policy validation

Before upgrading to the F-142 validation change, update existing ShutdownFlows and their GitOps
sources to `abortPolicy.behavior: HaltAndSurface`, `abortPolicy.notify: false`, and
`continueOnError: false` on every authored step. Set notification to false explicitly: older
defaulting stored true even when the field was omitted. These unsupported options previously had
no execution effect; v1 rejects them rather than promising continuation or an abort-notification
tail. Normal Notify actions and advisory hooks retain their behavior.

Apply these resource changes before upgrading the CRDs/operator. Admission rejects unsupported new
values, and the planner also rejects unchanged legacy objects that still contain them, so relying
on Kubernetes retaining an older object does not make its plan executable. Review compiled status
after upgrade. No resource deletion or automatic rewrite is required.

## Uninstall

Delete custom resources before uninstalling their CRDs. New and migrated `NUTServer` and
`NodePowerAgent` resources do not need a running manager to complete deletion. Kubernetes garbage
collection removes their owned workloads, ConfigMaps, generated credentials, signal Secrets,
Services, ServiceAccounts, NetworkPolicies, and disruption budgets. Shared operand namespaces,
user-supplied credential/TLS Secrets, and PostgreSQL audit data remain intentionally unowned.

Garbage collection is asynchronous; deletion is not an instantaneous cancellation of a shutdown
signal already projected into a running actuator. Disable actuation and let active executions
finish before uninstalling. Deletion does not promise a teardown Event or external audit record.

```sh
# 1. Your resources first, while the operator is still running.
kubectl delete shutdownflow --all
kubectl delete nodepoweragent --all
kubectl delete nutserver --all
kubectl delete upsdevice,powerinventoryedge,powerinventorynode,powerinfrastructure --all
kubectl delete powermanagementcluster --all

# 2. Then the operator. Use whichever bundle you installed.
kubectl delete -f https://raw.githubusercontent.com/MichaelZalud18/nut-operator/main/dist/install-byo-cert.yaml
```

Step 2 removes the CRDs, and with them any remaining custom resources. PostgreSQL audit data is not
touched — it outlives the operator on purpose. Deleting the namespace also removes the
`webhook-server-cert` and `nut-operator-webhook-ca` Secrets, so a later reinstall needs
`hack/webhook-cert.sh` run again.

If a legacy object is stuck in `Terminating`, run the upgraded manager to retire its obsolete key.
Inspect any remaining finalizers with their owning controller; do not clear the entire list,
because unrelated finalizers may protect real cleanup obligations.
