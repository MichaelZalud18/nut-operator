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

upsdrvctl start
exec upsd -D
