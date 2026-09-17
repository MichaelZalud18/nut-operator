#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo 'usage: nut-startup-smoke.sh <container-tool> <nut-server-image>' >&2
  exit 64
fi
container_tool="$1"
image="$2"
seconds="${NUT_STARTUP_SECONDS:-660}"
if [[ ! "$seconds" =~ ^[1-9][0-9]{0,2}$ ]] || (( seconds > 660 )); then
  echo 'NUT_STARTUP_SECONDS must be an integer from 1 to 660 (default 660)' >&2
  exit 64
fi
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
artifacts="$(mktemp -d "${NUT_STARTUP_ARTIFACT_ROOT:-${TMPDIR:-/tmp}}/nut-startup.XXXXXXXX")"
# Only this owned child is writable by container UID 65532. The private parent
# prevents other host users from traversing to its permissive mode.
chmod 0700 "$artifacts"
mkdir "$artifacts/observation"
chmod 0777 "$artifacts/observation"
container="nut-startup-${artifacts##*/}"
run_pid=
create_pid=
create_rc=125
create_attempted=0
container_id=
launching=0
cancel_rc=0
image_id=unknown
started=$SECONDS
cancel_run() {
  cancel_rc=$1
  if (( launching == 0 )); then exit "$cancel_rc"; fi
}
cleanup() {
  rc=$?
  trap - EXIT
  trap '' INT TERM
  set +e
  if [[ -n "$create_pid" ]]; then
    create_rc=0
    wait "$create_pid" || create_rc=$?
    create_pid=
  fi
  [[ ! -s "$artifacts/container.id" ]] || container_id="$(<"$artifacts/container.id")"
  creation_uncertain=0
  if (( create_attempted && create_rc != 0 )) || { (( create_attempted )) && [[ -z "$container_id" ]]; }; then
    creation_uncertain=1
    echo "container creation failed or timed out; cleanup remains uncertain: $container" >&2
    if (( rc == 0 )); then rc=1; fi
  fi
  if [[ -n "$run_pid" ]]; then
    kill -KILL "$run_pid" 2>/dev/null || true
    wait "$run_pid" 2>/dev/null || true
    run_pid=
  fi
  # Finalize observations before checking the private bind-mounted artifacts.
  stop_output="$(timeout -k 2s 12s "$container_tool" stop --time 8 "${container_id:-$container}" 2>&1)"
  if ! printf '%s\n' "$stop_output" >"$artifacts/stop.log"; then
    echo 'could not write startup artifact: stop.log' >&2
    [[ "$rc" -ne 0 ]] || rc=1
  fi
  container_output="$(timeout -k 2s 10s "$container_tool" logs "${container_id:-$container}" 2>&1)"
  if ! printf '%s\n' "$container_output" >"$artifacts/container.log"; then
    echo 'could not write startup artifact: container.log' >&2
    [[ "$rc" -ne 0 ]] || rc=1
  fi
  collected=0
  [[ -s "$artifacts/observation/summary.tsv" ]] && collected=1
  removed=0
  removal_output="$(timeout -k 2s 10s "$container_tool" rm -f "${container_id:-$container}" 2>&1)"
  if ! printf '%s\n' "$removal_output" >"$artifacts/cleanup.log"; then
    echo 'could not write startup artifact: cleanup.log' >&2
    [[ "$rc" -ne 0 ]] || rc=1
  fi
  remaining=unknown
  if remaining="$(timeout -k 2s 5s "$container_tool" ps -aq --filter "name=^${container//./\\.}$")" && [[ -z "$remaining" ]] && (( creation_uncertain == 0 )); then
    removed=1
  fi
  if (( rc == 0 && (collected == 0 || removed == 0) )); then rc=1; fi
  if ! printf 'scope\tsingle dummy UPS; eight local authenticated clients; not Kind or manager qualification\nimage_reference\t%s\nimage_id\t%s\nrequested_seconds\t%s\nhost_elapsed_seconds\t%s\nexit_code\t%s\nartifacts_collected\t%s\ncontainer_removed\t%s\ncreation_uncertain\t%s\ncontainer_id\t%s\n' \
    "$image" "$image_id" "$seconds" "$((SECONDS - started))" "$rc" "$collected" "$removed" "$creation_uncertain" "$container_id" >"$artifacts/run.tsv"; then
    echo 'could not write startup artifact: run.tsv' >&2
    [[ "$rc" -ne 0 ]] || rc=1
  fi
  echo "NUT startup observation artifacts: $artifacts (exit $rc)"
  exit "$rc"
}
trap cleanup EXIT
trap 'cancel_run 130' INT
trap 'cancel_run 143' TERM

image_id="$(timeout -k 2s 10s "$container_tool" image inspect --format '{{.Id}}' "$image")"
timeout -k 2s 10s "$container_tool" image inspect "$image_id" >"$artifacts/image.json"
echo "Observing $image_id for $seconds seconds; artifacts: $artifacts"
# Run by immutable local ID. Runtime state stays on private tmpfs mounts; only
# observations use the owned output mount. No host settings or external network.
create_attempted=1
launching=1
timeout -k 2s 10s "$container_tool" create --init \
  --name "$container" --network none --read-only --user 65532:65532 \
  --cap-drop ALL --security-opt no-new-privileges \
  --env "NUT_STARTUP_SECONDS=$seconds" \
  --tmpfs /tmp:rw,nosuid,nodev,uid=65532,gid=65532,mode=0700 \
  --tmpfs /run/nut:rw,nosuid,nodev,uid=65532,gid=65532,mode=0700 \
  --mount "type=bind,src=$artifacts/observation,dst=/tmp/observation" \
  --mount "type=bind,src=$root/hack/nut-startup-smoke-container.sh,dst=/startup-smoke.sh,readonly" \
  --entrypoint /bin/sh "$image_id" /startup-smoke.sh >"$artifacts/container.id" 2>"$artifacts/create.log" &
create_pid=$!
launching=0
if (( cancel_rc != 0 )); then exit "$cancel_rc"; fi
create_rc=0
wait "$create_pid" || create_rc=$?
create_pid=
if (( create_rc != 0 )); then exit "$create_rc"; fi
container_id="$(<"$artifacts/container.id")"
[[ -n "$container_id" ]] || exit 1
launching=1
timeout --signal=TERM --kill-after=5s "$((seconds + 30))s" "$container_tool" start --attach "$container_id" >"$artifacts/run.log" 2>&1 &
run_pid=$!
launching=0
if (( cancel_rc != 0 )); then exit "$cancel_rc"; fi
run_rc=0
wait "$run_pid" || run_rc=$?
run_pid=
exit "$run_rc"
