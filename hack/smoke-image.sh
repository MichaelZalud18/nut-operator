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
    "$container_tool" run --rm --entrypoint /bin/sh "$image" -ec '
      command -v upsd
      command -v upsdrvctl
      command -v upsc
      test -x /usr/lib/nut/dummy-ups || command -v dummy-ups
      upsd -V >/dev/null
      upsc -V >/dev/null
    '

    # F-47: the entrypoint must leave a PID file behind, not merely stay in the foreground.
    #
    # upsd has three flags that all foreground it -- -D (raise debugging level, foreground as a
    # side effect), -F (foreground), and -FF (foreground and save the PID file) -- and the running
    # process looks identical under all three. Only -FF writes /run/nut/upsd.pid, and `upsd -c
    # reload` signals a running process located through that file, so picking the wrong flag costs
    # the reload path silently (F-48). The file's existence is the one observable that tells them
    # apart, which is why this asserts on the file rather than on the entrypoint's text.
    smoke_config="$(mktemp -d)"
    trap 'rm -rf "$smoke_config"' EXIT
    printf '[smoke]\n  driver = dummy-ups\n  port = smoke.dev\n' > "$smoke_config/ups.conf"
    printf 'LISTEN 127.0.0.1 3493\n' > "$smoke_config/upsd.conf"
    printf '[smokeuser]\n  password = smoke\n  upsmon secondary\n' > "$smoke_config/upsd.users"
    # mktemp -d gives 0700 owned by the invoking user, which UID 65532 inside the container cannot
    # traverse. Without this the entrypoint reports "missing required /etc/nut/ups.conf" for a file
    # that exists and is populated -- the same misdirection F-51 records.
    chmod 0755 "$smoke_config"
    chmod 0644 "$smoke_config"/*

    # The driver has no device to talk to and will fail to start. That is deliberate: the
    # entrypoint is supposed to continue past it (NS-4), so this also proves upsd comes up on a
    # partial start rather than only on a clean one.
    #
    # Deliberately not --rm: if the container dies the failure path below needs its logs, and --rm
    # would take them with it.
    smoke_container="$("$container_tool" run -d -v "$smoke_config:/etc/nut:ro" "$image")"
    trap 'rm -rf "$smoke_config"; "$container_tool" rm -f "$smoke_container" >/dev/null 2>&1 || true' EXIT

    pidfile_found=""
    for _ in $(seq 1 20); do
      if "$container_tool" exec "$smoke_container" test -f /run/nut/upsd.pid 2>/dev/null; then
        pidfile_found="yes"
        break
      fi
      sleep 1
    done

    if [[ -z "$pidfile_found" ]]; then
      echo "nut-server smoke: upsd wrote no PID file at /run/nut/upsd.pid" >&2
      echo "  the entrypoint must run 'upsd -FF'; -D and -F both foreground without one (F-47)" >&2
      "$container_tool" logs "$smoke_container" >&2 || true
      exit 1
    fi
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
