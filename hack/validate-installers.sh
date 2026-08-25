#!/usr/bin/env bash
# Apply the committed installer bundles against a real API server.
#
# dist/install.yaml is the documented one-command install, and until this existed nothing ever
# handed it to an API server before a user did. installer-freshness only diffs the committed bytes
# against what the generator emits, so a bundle can be perfectly fresh and still be rejected on
# arrival -- which is exactly what F-113 was: three ShutdownHook ClusterRoles with an empty
# `resources:` list, accepted by every gate the project had and refused by the first real cluster,
# four image builds into an e2e run.
#
# Server-side, not client-side. `--dry-run=client` runs neither admission nor API validation and
# accepts the empty rule too; reaching the validation that rejects it is the entire point.
#
# The cluster is created and destroyed here rather than taken from the current context. Two of the
# steps below (the cert-manager CRDs, and the namespaces) write to whatever they are pointed at, and
# a script that silently provisions into a developer's real cluster because they forgot to switch
# contexts is not one worth running locally.
set -euo pipefail

KIND="${KIND:-kind}"
CLUSTER="${INSTALLER_CHECK_CLUSTER:-nut-operator-installer-check}"
BUNDLES=("$@")
if [ "${#BUNDLES[@]}" -eq 0 ]; then
  BUNDLES=(dist/install.yaml dist/install-byo-cert.yaml)
fi

cd "$(dirname "$0")/.."

for bundle in "${BUNDLES[@]}"; do
  [ -f "$bundle" ] || { echo "no such bundle: $bundle" >&2; exit 1; }
done

command -v "$KIND" >/dev/null 2>&1 || { echo "kind is not installed." >&2; exit 1; }

# A single-node default cluster on purpose: this needs an API server and nothing else. The e2e
# shape in test/e2e/kind-config.yaml costs minutes and buys nothing here.
cleanup() { "$KIND" delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true; }
trap cleanup EXIT
"$KIND" delete cluster --name "$CLUSTER" >/dev/null 2>&1 || true
"$KIND" create cluster --name "$CLUSTER"

kube() { kubectl --context "kind-$CLUSTER" "$@"; }

# dist/install.yaml carries cert-manager Certificate and Issuer objects. Without their CRDs the
# apply fails at RESTMapping -- "no matches for kind Certificate" -- before reaching any validation
# this script exists to run, so the bundle would be reported as rejected for a reason that says
# nothing about the bundle. Only the CRDs are installed: nothing here waits on a cert-manager
# controller, and a dry-run never asks one to issue a certificate.
#
# The version is read from test/utils/utils.go rather than pinned again here. The e2e suite already
# owns that pin, and a second copy of it is a second thing to forget.
cert_manager_version="$(sed -nE 's/^[[:space:]]*certmanagerVersion = "([^"]+)".*/\1/p' test/utils/utils.go)"
if [ -z "$cert_manager_version" ]; then
  echo "could not read certmanagerVersion from test/utils/utils.go" >&2
  exit 1
fi
echo "Installing cert-manager $cert_manager_version CRDs"
kube apply --server-side -f \
  "https://github.com/cert-manager/cert-manager/releases/download/${cert_manager_version}/cert-manager.crds.yaml"

# A server-side dry-run creates nothing, including the Namespace the bundle declares in its own
# first document -- so every namespaced object after it is rejected with "namespaces not found",
# and the bundle looks broken when it is not. The namespaces are the one part that has to be real.
#
# Extracted from the bundles rather than named here, for the same reason as the cert-manager pin:
# the manifests already say what the namespace is.
echo "Creating the namespaces the bundles declare"
python3 - "${BUNDLES[@]}" <<'PY' | kube apply --server-side -f -
import re
import sys

kept = []
for path in sys.argv[1:]:
    with open(path, encoding="utf-8") as handle:
        for doc in handle.read().split("\n---\n"):
            if re.search(r"^kind: Namespace$", doc, re.M) and doc not in kept:
                kept.append(doc)
if not kept:
    sys.exit("no Namespace document found in any bundle")
sys.stdout.write("\n---\n".join(kept))
PY

status=0
for bundle in "${BUNDLES[@]}"; do
  echo "::group::$bundle"
  if ! kube apply --server-side --dry-run=server -f "$bundle"; then
    echo "::error::$bundle was rejected by the API server"
    status=1
  fi
  echo "::endgroup::"
done

if [ "$status" -eq 0 ]; then
  echo "Both installers were accepted by the API server."
fi
exit "$status"
