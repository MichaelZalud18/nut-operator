# Upgrade and uninstall

Components: Cross-cutting.
Audience: operators.

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

# 2. Then the operator. Use whichever bundle you installed.
kubectl delete -f https://raw.githubusercontent.com/MichaelZalud18/nut-operator/main/dist/install-byo-cert.yaml
```

Step 2 removes the CRDs, and with them any remaining custom resources. PostgreSQL audit data is not
touched — it outlives the operator on purpose. Deleting the namespace also removes the
`webhook-server-cert` and `nut-operator-webhook-ca` Secrets, so a later reinstall needs
`hack/webhook-cert.sh` run again.

If something is already stuck in `Terminating`, reinstall the operator and let it finish, or clear
the finalizer by hand:

```sh
kubectl patch nutserver <name> --type=merge -p '{"metadata":{"finalizers":[]}}'
```
