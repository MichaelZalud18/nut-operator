#!/bin/sh
set -eu

if [ "$#" -gt 0 ]; then
  exec "$@"
fi

if [ ! -s /etc/nut/ups.conf ]; then
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
# probe (upsc -l) already reports "not ready" correctly when no driver has registered.
if ! upsdrvctl start; then
  echo "one or more NUT drivers failed to start; continuing so upsd still serves any devices that did register" >&2
fi

exec upsd -D
