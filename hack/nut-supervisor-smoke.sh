#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo 'usage: nut-supervisor-smoke.sh <container-tool> <nut-server-image>' >&2
  exit 64
fi

container_tool="$1"
image="$2"
samples="${NUT_READINESS_SAMPLES:-0}"
diagnostics="${NUT_READINESS_DIAGNOSTICS:-0}"
if [[ "$diagnostics" != 0 && "$diagnostics" != 1 ]]; then
  echo 'NUT_READINESS_DIAGNOSTICS must be 0 or 1' >&2
  exit 64
fi
if [[ ! "$samples" =~ ^(0|[1-9][0-9]?)$ ]] || (( samples > 60 )); then
  echo 'NUT_READINESS_SAMPLES must be an integer from 0 to 60' >&2
  exit 64
fi
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
identity="$(mktemp -d)"
container="nut-supervisor-${identity##*/}"
run_pid=
cleanup() {
  trap '' INT TERM
  "$container_tool" rm -f "$container" >/dev/null 2>&1 || true
  if [[ -n "$run_pid" ]]; then
    kill "$run_pid" 2>/dev/null || true
    wait "$run_pid" 2>/dev/null || true
  fi
  rmdir "$identity"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# All mutable state is container-private. No cluster, host namespace, or external network access.
timeout --signal=TERM --kill-after=5s 180s "$container_tool" run --rm --init \
  --name "$container" --network none --read-only --user 65532:65532 \
  --cap-drop ALL --security-opt no-new-privileges \
  --env "NUT_READINESS_SAMPLES=$samples" \
  --env "NUT_READINESS_DIAGNOSTICS=$diagnostics" \
  --tmpfs /tmp:rw,nosuid,nodev,uid=65532,gid=65532,mode=0700 \
  --tmpfs /run/nut:rw,nosuid,nodev,uid=65532,gid=65532,mode=0700 \
  --mount "type=bind,src=$root/hack/nut-supervisor-smoke-container.sh,dst=/smoke.sh,readonly" \
  --mount "type=bind,src=$root/hack/nut-readiness-stress-container.sh,dst=/probe-stress.sh,readonly" \
  --mount "type=bind,src=$root/hack/nut-readiness-diagnostic-container.sh,dst=/probe-diagnostic.sh,readonly" \
  --entrypoint /bin/sh "$image" /smoke.sh &
run_pid=$!
# Bash runs traps promptly while waiting on a background job, not a foreground command.
wait "$run_pid"
