#!/bin/sh
set -eu

export NUT_CONFPATH=/tmp/nut-readiness
export NUT_QUIET_INIT_BANNER=true
checker=/usr/local/bin/nut-driver-ready
healthy_pid=
frozen_pid=
cleanup() {
  rc=$?
  trap - EXIT
  if [ "$rc" -ne 0 ]; then
    cat "$NUT_CONFPATH/ups.conf" /tmp/readiness-probe.log /tmp/readiness-healthy.log /tmp/readiness-frozen.log >&2 || true
  fi
  for pid in "$healthy_pid" "$frozen_pid"; do
    [ -n "$pid" ] || continue
    kill -CONT "$pid" 2>/dev/null || true
    kill -TERM "$pid" 2>/dev/null || true
  done
  for pid in "$healthy_pid" "$frozen_pid"; do
    [ -z "$pid" ] || wait "$pid" 2>/dev/null || true
  done
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

mkdir -p "$NUT_CONFPATH"
if [ ! -x "$checker" ]; then
  echo "readiness regression requires the shipped $checker binary" >&2
  exit 1
fi
printf 'ups.status: OL\nbattery.runtime: 3600\nbattery.charge: 100\n' > "$NUT_CONFPATH/good.dev"

write_config() {
  : > "$NUT_CONFPATH/ups.conf.next"
  for name in "$@"; do
    printf '[%s]\n driver = dummy-ups\n port = %s/good.dev\n' "$name" "$NUT_CONFPATH" >> "$NUT_CONFPATH/ups.conf.next"
  done
  mv "$NUT_CONFPATH/ups.conf.next" "$NUT_CONFPATH/ups.conf"
}

probe() {
  expected=$1
  description=$2
  probe_rc=0
  # The checker's global deadline is four seconds. A watchdog expiry is never an
  # expected negative result, including BusyBox timeout's signal-based exit.
  timeout -k 1 5 "$checker" >/tmp/readiness-probe.log 2>&1 || probe_rc=$?
  if [ "$probe_rc" -ne "$expected" ]; then
    echo "readiness regression failed: $description (exit=$probe_rc, expected=$expected)" >&2
    cat /tmp/readiness-probe.log >&2
    return 1
  fi
  echo "readiness regression passed: $description"
}

drivers_started() {
  [ -S /run/nut/dummy-ups-healthy ] && [ -S /run/nut/dummy-ups-frozen ] &&
    [ -s /run/nut/dummy-ups-healthy.pid ] && [ -s /run/nut/dummy-ups-frozen.pid ]
}
same_pids() {
  test "$(cat /run/nut/dummy-ups-healthy.pid)" = "$healthy_pid"
  test "$(cat /run/nut/dummy-ups-frozen.pid)" = "$frozen_pid"
  kill -0 "$healthy_pid"
  kill -0 "$frozen_pid"
}
is_frozen() { grep -q '^State:.*T' "/proc/$frozen_pid/status"; }
freeze_driver() {
  target_pid=$1
  kill -STOP "$target_pid"
  for attempt in $(seq 1 20); do
    if grep -q '^State:.*T' "/proc/$target_pid/status"; then return 0; fi
    sleep 0.1
  done
  echo 'driver did not enter the stopped state' >&2
  return 1
}

write_config absent
probe 1 'configured driver socket absent'
write_config
probe 1 'no configured drivers'

write_config healthy frozen
upsdrvctl -FF start healthy >/tmp/readiness-healthy.log 2>&1 &
healthy_pid=$!
upsdrvctl -FF start frozen >/tmp/readiness-frozen.log 2>&1 &
frozen_pid=$!
for attempt in $(seq 1 20); do
  if drivers_started; then break; fi
  kill -0 "$healthy_pid"
  kill -0 "$frozen_pid"
  sleep 1
done
drivers_started
same_pids

# Restrict only the checker's configuration; both real foreground drivers stay
# running. An unconfigured healthy socket must not mask the stopped target.
write_config healthy
probe 0 'healthy real driver baseline'
write_config frozen
probe 0 'second real driver baseline'
freeze_driver "$frozen_pid"
probe 1 'STOP-confirmed driver despite an unconfigured healthy socket'
is_frozen
same_pids
kill -CONT "$frozen_pid"
probe 0 'resumed real driver, same PID'
same_pids

freeze_driver "$frozen_pid"
write_config frozen healthy
probe 0 'frozen then healthy configured drivers'
is_frozen
same_pids
write_config healthy frozen
probe 0 'healthy then frozen configured drivers'
is_frozen
same_pids

# Two unresponsive sockets still share one deadline; a per-driver four-second
# timeout would reach the watchdog and must fail this regression.
freeze_driver "$healthy_pid"
probe 1 'both real drivers frozen within one global deadline'
is_frozen
grep -q '^State:.*T' "/proc/$healthy_pid/status"
same_pids
echo 'NUT readiness image regression passed'
