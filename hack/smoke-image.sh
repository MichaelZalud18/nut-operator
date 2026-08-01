#!/usr/bin/env bash
set -euo pipefail

if [[ "$#" -ne 3 ]]; then
  echo "usage: smoke-image.sh <container-tool> <kind> <image>" >&2
  exit 64
fi

container_tool="$1"
kind="$2"
image="$3"

case "$kind" in
  nut-server)
    exec "$container_tool" run --rm --entrypoint /bin/sh "$image" -ec '
      command -v upsd
      command -v upsdrvctl
      command -v upsc
      test -x /usr/lib/nut/dummy-ups || command -v dummy-ups
      upsd -V >/dev/null
      upsc -V >/dev/null
    '
    ;;
  upsmon-agent)
    exec "$container_tool" run --rm --entrypoint /bin/sh "$image" -ec '
      command -v upsmon
      command -v upsc
      upsmon -V >/dev/null
      upsc -V >/dev/null
    '
    ;;
  node-actuator)
    exec "$container_tool" run --rm "$image" --version
    ;;
  *)
    echo "unknown smoke image kind: $kind" >&2
    exit 64
    ;;
esac
