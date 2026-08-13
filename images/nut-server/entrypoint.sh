#!/bin/sh
set -eu

if [ "$#" -gt 0 ]; then
  exec "$@"
fi

# ups.conf must exist, but an empty one is a legitimate state: a NUTServer whose device selector
# matches nothing yet renders exactly that (F-51). The check is therefore existence, not content.
#
# It used to be `-s`, which fails on an empty file and reported "missing required
# /etc/nut/ups.conf" for a file that was present and was exactly what the operator meant to write.
# That sent diagnosis toward a broken mount instead of an empty selector. upsd handles the empty
# case itself once ALLOW_NO_DEVICE is set, which the operator now always renders.
if [ ! -f /etc/nut/ups.conf ]; then
  echo "missing required /etc/nut/ups.conf" >&2
  exit 1
fi

if [ ! -s /etc/nut/upsd.conf ]; then
  echo "missing required /etc/nut/upsd.conf" >&2
  exit 1
fi

if [ ! -s /etc/nut/upsd.users ]; then
  echo "missing required /etc/nut/upsd.users" >&2
  exit 1
fi

mkdir -p /run/nut

# A driver that fails to start (bad/missing credentials, unreachable UPS, etc.) must not take
# upsd down with it. upsd should come up regardless so devices that did register stay queryable,
# and so credentials can be wired in / corrected without restarting the server. The readiness
# probe reads `upsdrvctl status` and already reports "not ready" correctly when no driver is
# responsive, so a partial start surfaces as an unready pod rather than a crash loop.
if ! upsdrvctl start; then
  echo "one or more NUT drivers failed to start; continuing so upsd still serves any devices that did register" >&2
fi

# -FF, not -D (F-47). All three of -D, -F and -FF keep upsd in the foreground, which is what a
# container needs, but they are not interchangeable:
#
#   -D    raise debugging level (and stay foreground by default)
#   -F    stay foregrounded even if no debugging is enabled
#   -FF   stay foregrounded and still save the PID file
#
# Under -D the foregrounding is a side effect of turning debugging on, so the operand ran at debug
# level permanently on every cluster. -FF asks for the behavior directly and additionally writes
# /run/nut/upsd.pid -- under -F, upsd logs "Running as foreground process, not saving a PID file".
#
# The PID file is not incidental. `upsd -c reload` signals a running process located through it,
# which is the only way to re-read configuration without replacing the pod and dropping every
# upsmon session (F-48). It lands in /run/nut via --with-altpidpath, already a writable emptyDir
# holding the driver sockets, so the read-only root filesystem is unaffected.
exec upsd -FF
