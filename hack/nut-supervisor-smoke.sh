#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo 'usage: nut-supervisor-smoke.sh <container-tool> <nut-server-image>' >&2
  exit 64
fi

container_tool="$1"
image="$2"
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
identity="$(mktemp -d)"
container="nut-supervisor-${identity##*/}"
cleanup() {
  "$container_tool" rm -f "$container" >/dev/null 2>&1 || true
  rmdir "$identity"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# All mutable state is container-private. No cluster, host namespace, or external network access.
timeout --signal=TERM --kill-after=5s 180s "$container_tool" run --rm --init \
  --name "$container" --network none --read-only --user 65532:65532 \
  --cap-drop ALL --security-opt no-new-privileges \
  --tmpfs /tmp:rw,nosuid,nodev,uid=65532,gid=65532,mode=0700 \
  --tmpfs /run/nut:rw,nosuid,nodev,uid=65532,gid=65532,mode=0700 \
  --mount "type=bind,src=$root/internal/nutsupervisor/supervisor.sh,dst=/supervisor.sh,readonly" \
  --mount "type=bind,src=$root/hack/nut-supervisor-smoke-container.sh,dst=/smoke.sh,readonly" \
  --entrypoint /bin/sh "$image" /smoke.sh
