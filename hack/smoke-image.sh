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
    # ALLOWLISTED_DRIVERS must match supportedNetworkUPSDrivers() in
    # internal/webhook/v1alpha1/upsdevice_webhook.go. TestSmokeTestCoversEveryAllowlistedDriver
    # parses this line and fails when the two disagree.
    #
    # Admission accepting a driver the image does not contain produces a UPSDevice that is valid,
    # renders into ups.conf, and can never start -- which is how powerman-pdu survived in the
    # allowlist across a source-build migration that dropped it from the image (F-50). Asserting
    # the binaries here is the half of the guard that can see the image.
    ALLOWLISTED_DRIVERS="dummy-ups snmp-ups netxml-ups apcupsd-ups"

    "$container_tool" run --rm --entrypoint /bin/sh "$image" -ec '
      command -v upsd
      command -v upsdrvctl
      command -v upsc
      upsd -V >/dev/null
      upsc -V >/dev/null
      for driver in '"$ALLOWLISTED_DRIVERS"'; do
        if [ ! -x "/usr/lib/nut/$driver" ]; then
          echo "admission allowlists $driver but the image does not contain it (F-50)" >&2
          exit 1
        fi
      done
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

    # F-51: a NUTServer whose device selector matches nothing renders an empty ups.conf, and that
    # has to be a running-but-idle server rather than a crash loop.
    #
    # Two independent things could break it and both did: the entrypoint's non-empty test, which
    # reported "missing required /etc/nut/ups.conf" for a file that was present and correct, and
    # upsd itself, which calls fatalx on a device-less ups.conf unless ALLOW_NO_DEVICE is set. The
    # operator renders that directive unconditionally; this asserts the image honors it.
    printf '' > "$smoke_config/ups.conf"
    printf 'LISTEN 127.0.0.1 3493\nALLOW_NO_DEVICE true\n' > "$smoke_config/upsd.conf"

    empty_container="$("$container_tool" run -d -v "$smoke_config:/etc/nut:ro" "$image")"
    trap 'rm -rf "$smoke_config"; "$container_tool" rm -f "$smoke_container" "$empty_container" >/dev/null 2>&1 || true' EXIT
    sleep 5

    if [[ "$("$container_tool" inspect --format '{{.State.Running}}' "$empty_container")" != "true" ]]; then
      echo "nut-server smoke: a device-less ups.conf must start an idle server, not fail (F-51)" >&2
      "$container_tool" logs "$empty_container" >&2 || true
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
    "$container_tool" run --rm "$image" --version

    # F-61: the actuator must carry CAP_SYS_BOOT as a file capability on the binary.
    #
    # Asking for the capability in the container securityContext is not enough and fails silently.
    # Kubernetes puts it in the bounding set, but the pod runs as UID 65532 and the permitted set
    # does not survive that transition, so the process comes up with CapPrm and CapEff both zero
    # and reboot(2) returns EPERM. Nothing reports that until the one moment it matters, and
    # actuation has only ever run stubbed, so no test would have caught it.
    #
    # Checked from outside because the image is distroless and has no shell: a throwaway stage
    # copies the shipped binary out and reads the extended attribute. That also exercises the thing
    # most likely to break it, which is a COPY that does not preserve xattrs.
    capability_context="$(mktemp -d)"
    trap 'rm -rf "$capability_context"' EXIT
    cat > "$capability_context/Dockerfile" <<DOCKERFILE
FROM $image AS shipped
FROM alpine:3.22
RUN apk add --no-cache libcap
COPY --from=shipped /node-actuator /check/node-actuator
RUN getcap /check/node-actuator | grep -q cap_sys_boot || \
    (echo "node-actuator ships without cap_sys_boot; reboot(2) would fail with EPERM (F-61)" >&2; exit 1)
DOCKERFILE

    if ! "$container_tool" build --quiet -f "$capability_context/Dockerfile" "$capability_context" >/dev/null; then
      echo "node-actuator smoke: the shipped binary is missing cap_sys_boot=ep (F-61)" >&2
      exit 1
    fi
    ;;
  *)
    echo "unknown smoke image kind: $kind" >&2
    exit 64
    ;;
esac
