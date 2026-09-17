#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo 'usage: nut-driver-ready-smoke.sh <container-tool> <nut-server-image>' >&2
  exit 64
fi

container_tool="$1"
image="$2"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
identity="$(mktemp -d)"
container="nut-driver-ready-${identity##*/}"
run_pid=
create_pid=
create_rc=125
create_attempted=0
container_id=
launching=0
cancel_rc=0
cancel_run() {
  cancel_rc=$1
  if (( launching == 0 )); then exit "$cancel_rc"; fi
}
cleanup() {
  rc=$?
  trap - EXIT
  trap '' INT TERM
  set +e
  # Do not interrupt an accepted create request: its existing timeout owns the
  # bound. Settling it before removal prevents a late-created running container.
  if [[ -n "$create_pid" ]]; then
    create_rc=0
    wait "$create_pid" || create_rc=$?
    create_pid=
  fi
  [[ ! -s "$identity/container.id" ]] || container_id="$(<"$identity/container.id")"
  if [[ -n "$run_pid" ]]; then
    kill -KILL "$run_pid" 2>/dev/null || true
    wait "$run_pid" 2>/dev/null || true
  fi
  if (( create_attempted )); then
    timeout -k 2s 10s "$container_tool" rm -f "${container_id:-$container}" >/dev/null 2>&1 || true
  fi
  # --rm can remove the container before explicit cleanup. Require a successful
  # daemon query and an empty exact-name result, not merely a failed inspect.
  remaining=unknown
  if ! remaining="$(timeout -k 2s 5s "$container_tool" ps -aq \
    --filter "name=^${container//./\\.}$")" || [[ -n "$remaining" ]]; then
    echo "could not confirm removal of owned readiness container: $container" >&2
    if (( rc == 0 )); then rc=1; fi
  fi
  if (( create_attempted && create_rc != 0 )) || { (( create_attempted )) && [[ -z "$container_id" ]]; }; then
    echo "container creation failed or timed out; cleanup remains uncertain: $container" >&2
    cat "$identity/create.log" >&2
    if (( rc == 0 )); then rc=1; fi
  fi
  rm -f "$identity/container.id" "$identity/create.log"
  rmdir "$identity"
  exit "$rc"
}
trap cleanup EXIT
trap 'cancel_run 130' INT
trap 'cancel_run 143' TERM

create_attempted=1
launching=1
timeout -k 2s 10s "$container_tool" create --init \
  --name "$container" --network none --read-only --user 65532:65532 \
  --cap-drop ALL --security-opt no-new-privileges \
  --tmpfs /tmp:rw,nosuid,nodev,uid=65532,gid=65532,mode=0700 \
  --tmpfs /run/nut:rw,nosuid,nodev,uid=65532,gid=65532,mode=0700 \
  --mount "type=bind,src=$root/hack/nut-driver-ready-smoke-container.sh,dst=/readiness-smoke.sh,readonly" \
  --entrypoint /bin/sh "$image" /readiness-smoke.sh >"$identity/container.id" 2>"$identity/create.log" &
create_pid=$!
launching=0
if (( cancel_rc != 0 )); then exit "$cancel_rc"; fi
create_rc=0
wait "$create_pid" || create_rc=$?
create_pid=
if (( create_rc != 0 )); then exit "$create_rc"; fi
container_id="$(<"$identity/container.id")"
[[ -n "$container_id" ]] || exit 1
# Starting an existing ID cannot create a replacement after cleanup.
launching=1
timeout --signal=TERM --kill-after=5s 60s "$container_tool" start --attach "$container_id" &
run_pid=$!
launching=0
if (( cancel_rc != 0 )); then exit "$cancel_rc"; fi
run_rc=0
wait "$run_pid" || run_rc=$?
run_pid=
exit "$run_rc"
