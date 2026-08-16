#!/usr/bin/env bash
# Prove that this cluster can actually halt a node.
#
# Everything the actuate path renders -- hostPID, CAP_SYS_BOOT as a permitted-only file capability,
# the Pod Security posture -- is exercised by envtest only as a rendered pod spec. Envtest never
# asks kubelet to accept that spec, and the actuate branch is exactly where kubelet has opinions.
#
# Five things only a real run can prove:
#   1. kubelet admits the pod under the namespace's Pod Security Admission level.
#   2. The binary's cap_sys_boot file capability survived the image build and the registry round
#      trip. The security.capability extended attribute is silently dropped by some builders and
#      some registries.
#   3. raiseSysBoot() moves the capability from permitted into effective.
#   4. The process is genuinely in the host PID namespace. This one cannot be checked any other way:
#      from a non-initial PID namespace, reboot(2) with POWER_OFF returns success and does nothing.
#      A node that actually goes dark is the only available proof.
#   5. The delivery chain under real timing: operator writes the Secret, kubelet projects it, the
#      actuator polls, validates, and acts.
#
# The signal is hand-delivered into the projected Secret, which is the same path OD-37 authorizes --
# this is not a second authorization channel and must not become one. It bypasses the planner
# deliberately, so planner correctness (which has its own tests) cannot fail this one.
set -euo pipefail

NODE="${NODE:-}"
NAMESPACE="${NAMESPACE:-power-system}"
AGENT="${AGENT:-}"
APPROVE="${APPROVE:-}"
TTL_SECONDS="${TTL_SECONDS:-120}"

fail() { echo "error: $*" >&2; exit 1; }
step() { printf '[%s] %s\n' "$(date -u +%H:%M:%SZ)" "$*"; }

[ -n "$NODE" ] || fail "NODE is required and has no default. Pass the node to power off: make verify-actuation NODE=<node>"
[ -n "$AGENT" ] || fail "AGENT is required: the NodePowerAgent whose signal Secret carries this node."
command -v kubectl >/dev/null || fail "kubectl is not on PATH."

kubectl get node "$NODE" >/dev/null 2>&1 || fail "node $NODE not found in this cluster."

cat <<WARNING

  ============================================================================
   THIS POWERS OFF A REAL MACHINE AND LEAVES IT OFF
  ============================================================================

   Node:       $NODE
   Agent:      $AGENT (namespace $NAMESPACE)

   What happens:
     - $NODE is sent a real shutdown signal and halts via reboot(2).
     - It STAYS OFF. Nothing in this project turns it back on.
     - You need physical access, or IPMI/BMC/iLO/iDRAC, to bring it back.
     - It returns cordoned. Uncordoning is a manual step you perform later.

   Restart was considered and rejected rather than overlooked: a restarted
   node comes back, gets a fresh actuator with an empty dedupe set, and can
   halt again if the signal is still inside its TTL. A node that is off
   stays off, so a leftover signal is inert.

  ============================================================================

WARNING

if [ "$APPROVE" != "yes" ]; then
  fail "refusing to proceed without APPROVE=yes. Re-run with APPROVE=yes once the warning above is understood."
fi

# Drain gate. Running workloads should not be discovered by having their node vanish.
step "checking $NODE is cordoned and drained"
schedulable="$(kubectl get node "$NODE" -o jsonpath='{.spec.unschedulable}' 2>/dev/null || true)"
if [ "$schedulable" != "true" ]; then
  fail "$NODE is not cordoned. Run: kubectl cordon $NODE && kubectl drain $NODE --ignore-daemonsets --delete-emptydir-data"
fi
remaining="$(kubectl get pods --all-namespaces --field-selector "spec.nodeName=$NODE" \
  -o jsonpath='{range .items[?(@.metadata.ownerReferences[0].kind!="DaemonSet")]}{.metadata.namespace}/{.metadata.name} {end}' 2>/dev/null || true)"
if [ -n "${remaining// /}" ]; then
  fail "$NODE still runs non-DaemonSet pods: $remaining"
fi

step "verifying the agent renders the actuate configuration"
mode="$(kubectl get nodepoweragent "$AGENT" -n "$NAMESPACE" -o jsonpath='{.spec.mode}')"
policy="$(kubectl get nodepoweragent "$AGENT" -n "$NAMESPACE" -o jsonpath='{.spec.shutdown.actuatorPolicy}')"
[ "$mode" = "Actuate" ] || fail "agent $AGENT is mode=$mode; this procedure verifies the actuate path and needs mode=Actuate."
[ "$policy" = "PowerOff" ] || fail "agent $AGENT is actuatorPolicy=$policy; needs PowerOff. Simulate issues no syscall and would prove nothing."

secret="${AGENT}-node-signals"
step "confirming the actuator pod on $NODE is Ready (kubelet admitted the actuate pod spec)"
pod="$(kubectl get pods -n "$NAMESPACE" --field-selector "spec.nodeName=$NODE" \
  -l "app.kubernetes.io/name=nut-operator" -o name 2>/dev/null | head -1)"
[ -n "$pod" ] || fail "no agent pod found on $NODE. If kubelet rejected the actuate pod spec, that is itself the finding -- check Pod Security Admission on namespace $NAMESPACE."
kubectl wait --for=condition=Ready -n "$NAMESPACE" "$pod" --timeout=60s \
  || fail "agent pod on $NODE is not Ready. Its readiness probe reflects the actuator watch loop, so this is a real failure and not a timing artefact."

step "writing the verification signal into $secret"
payload="$(cat <<JSON
{"executionID":"verify-$(date -u +%Y%m%d%H%M%S)","nodeName":"$NODE","planConfigHash":"verification","reason":"OperatorVerification","selectedUPSDevices":[],"shutdownFlow":"actuation-verification","timestamp":"$(date -u +%Y-%m-%dT%H:%M:%S.%NZ)"}
JSON
)"
encoded="$(printf '%s' "$payload" | base64 -w0)"
kubectl patch secret "$secret" -n "$NAMESPACE" --type merge \
  -p "{\"data\":{\"${NODE}.json\":\"${encoded}\"}}" >/dev/null \
  || fail "could not write the signal into $secret."

step "signal written. The operator keeps a signal naming an unknown flow until its TTL expires,"
step "so it will not be revoked mid-test and will be cleaned up after ~${TTL_SECONDS}s."
step "watching $NODE (expect NotReady, then gone)"

deadline=$(( $(date +%s) + TTL_SECONDS + 120 ))
while [ "$(date +%s)" -lt "$deadline" ]; do
  status="$(kubectl get node "$NODE" -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}' 2>/dev/null || echo "gone")"
  step "  node Ready=$status"
  if [ "$status" != "True" ]; then
    cat <<'DONE'

  The node stopped reporting Ready. That is the expected outcome and it is the
  only available proof of the host PID namespace: from a non-initial PID
  namespace reboot(2) returns success and the node stays up.

  Confirm the machine is actually powered off, not merely unreachable, before
  recording this as a pass. Then bring it back out of band and uncordon it.

DONE
    exit 0
  fi
  sleep 10
done

fail "$NODE still reports Ready after the signal TTL. The actuator did not halt it. Check the actuator container log on $NODE for which gate it stopped at -- capability check, permitted-to-effective raise, or the syscall itself."
