#!/usr/bin/env bash
set -euo pipefail

# One disposable API/GC cluster, no operator installation or host actuation.
cd "$(dirname "$0")/.."
umask 077
state=$(mktemp -d "${TMPDIR:-/tmp}/nut-operand-deletion.XXXXXXXX")
cluster="nut-deletion-$(basename "$state" | tr '[:upper:]' '[:lower:]' | tr -cd 'a-z0-9' | tail -c 8)"
export KUBECONFIG="$state/kubeconfig"
export KUBECTL_KUBERC=false
created=false
cleanup() {
  local result=$?
  if [[ "$created" == true ]]; then
    timeout --signal=TERM --kill-after=10s 90s kind delete cluster --name "$cluster" || result=1
  fi
  rm -rf -- "$state"
  exit "$result"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
clusters=$(timeout --signal=TERM --kill-after=5s 15s kind get clusters)
if grep -Fxq "$cluster" <<< "$clusters"; then
  echo "Refusing existing cluster $cluster" >&2
  exit 1
fi
# Matches the reviewed Kubernetes release used by the Kind scenario suite.
image="kindest/node:v1.34.0@sha256:7416a61b42b1662ca6ca89f02028ac133a309a2a30ba309614e8ec94d976dc5a"
created=true
timeout --signal=TERM --kill-after=10s 180s kind create cluster --name "$cluster" --image "$image" --kubeconfig "$KUBECONFIG" --wait 90s
test "$(kubectl config current-context)" = "kind-$cluster"
export USE_EXISTING_CLUSTER=true
export OPERAND_GC_ACCEPTANCE=true
timeout --signal=TERM --kill-after=10s 180s go test ./internal/controller -run '^TestControllers$' -count=1 -timeout=150s \
  -v -ginkgo.fail-on-empty \
  -ginkgo.focus='deletes operands without an operator deletion reconcile|retires legacy finalizers on live and terminating objects'
