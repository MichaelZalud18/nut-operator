#!/bin/sh
set -eu

export NUT_CONFPATH=/tmp/nut
export NUT_SUPERVISOR_INTERVAL_SECONDS=1
export NUT_QUIET_INIT_BANNER=true
mkdir -p "$NUT_CONFPATH"
supervisor_pid=
server_pid=
cleanup() {
  rc=$?
  trap - EXIT
  if [ "$rc" -ne 0 ]; then
    cat /tmp/supervisor.log /tmp/upsd.log >&2 || true
    upsdrvctl status >&2 || true
  fi
  [ -z "$supervisor_pid" ] || kill "$supervisor_pid" 2>/dev/null || true
  [ -z "$server_pid" ] || kill "$server_pid" 2>/dev/null || true
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 143' TERM

await() {
  description=$1
  shift
  for attempt in $(seq 1 20); do
    if "$@"; then return 0; fi
    sleep 1
  done
  echo "timed out: $description" >&2
  return 1
}

write_config() {
  : > "$NUT_CONFPATH/ups.conf.next"
  for name in "$@"; do
    port="$NUT_CONFPATH/good.dev"
    [ "$name" != bad ] || port="$NUT_CONFPATH/missing.dev"
    printf '[%s]\n  driver = dummy-ups\n  port = %s\n' "$name" "$port" >> "$NUT_CONFPATH/ups.conf.next"
  done
  mv "$NUT_CONFPATH/ups.conf.next" "$NUT_CONFPATH/ups.conf"
}

responsive() { [ "$(upsc "$1@127.0.0.1" ups.status 2>/dev/null)" = OL ]; }
pid_file() { printf '/run/nut/dummy-ups-%s.pid' "$1"; }
same_good_pid() { [ "$(cat "$(pid_file good)")" = "$good_pid" ]; }
worker_pid() {
  awk -v ups="ups=$1" '/Started driver/ {
      matched=0; candidate=""
      for (i=1; i<=NF; i++) {
        if ($i == ups) matched=1
        if ($i ~ /^pid=/) { candidate=$i; sub(/^pid=/, "", candidate) }
      }
      if (matched) pid=candidate
  } END { if (pid == "") exit 1; print pid }' /tmp/supervisor.log
}
start_supervisor() {
  /usr/local/bin/nut-driver-supervisor >>/tmp/supervisor.log 2>&1 &
  supervisor_pid=$!
}
removed() { ! responsive second && ! kill -0 "$second_pid" 2>/dev/null; }
recovered() {
  [ -s "$(pid_file good)" ] &&
    [ "$(cat "$(pid_file good)")" != "$old_pid" ] && responsive good
}
workers_gone() { ! pidof dummy-ups upsdrvctl >/dev/null; }
reload_retried() { [ "$(grep -ic 'upsd reload failed, will retry' /tmp/supervisor.log)" -ge 2 ]; }
bad_retried() { grep -q 'Driver exited.*ups=bad' /tmp/supervisor.log; }

printf 'ups.status: OL\nbattery.runtime: 3600\nbattery.charge: 100\n' > "$NUT_CONFPATH/good.dev"
printf 'LISTEN 127.0.0.1 3493\nALLOW_NO_DEVICE true\n' > "$NUT_CONFPATH/upsd.conf"
printf '[test]\n  password = component-test-only\n  upsmon secondary\n' > "$NUT_CONFPATH/upsd.users"
write_config
upsd -FF >/tmp/upsd.log 2>&1 &
server_pid=$!
start_supervisor
await 'idle upsd startup' test -s /run/nut/upsd.pid
kill -0 "$supervisor_pid"

write_config good bad
await 'healthy driver beside invalid definition' responsive good
await 'failed driver is retried' bad_retried
good_pid="$(cat "$(pid_file good)")"
good_worker="$(worker_pid good)"
# Make the real reload command fail without killing the server or replacing any NUT binary.
mv /run/nut/upsd.pid /run/nut/upsd.pid.saved
write_config good second bad
await 'failed upsd reload is retried' reload_retried
same_good_pid
test ! -e "$(pid_file second)"
mv /run/nut/upsd.pid.saved /run/nut/upsd.pid
await 'new driver becomes visible through upsd reload' responsive second
same_good_pid
test "$(worker_pid good)" = "$good_worker"
second_pid="$(cat "$(pid_file second)")"
write_config good
await 'removed driver disappears from upsd and supervisor' removed
same_good_pid
test "$(worker_pid good)" = "$good_worker"

# NUT reports a malformed section as "no UPS definitions", just like an empty file.
printf '[unterminated\n' > "$NUT_CONFPATH/ups.conf.next"
mv "$NUT_CONFPATH/ups.conf.next" "$NUT_CONFPATH/ups.conf"
await 'malformed configuration is refused' grep -q 'keeping existing workers' /tmp/supervisor.log
same_good_pid
responsive good
test "$(worker_pid good)" = "$good_worker"
write_config good

test_driver_reload() {
  # Observe actual upstream reload decisions, not just a successful signal command.
  write_config good second
  await 'second driver for reload isolation' responsive second
  second_pid="$(cat "$(pid_file second)")"
  printf 'ups.status: OL\nbattery.runtime: 1800\n' > "$NUT_CONFPATH/changed.dev"
  write_reload_config() {
    printf '[good]\n driver = %s\n port = %s\n debug_min = 1\n[second]\n driver = dummy-ups\n port = %s/good.dev\n' \
      "$1" "$2" "$NUT_CONFPATH" > "$NUT_CONFPATH/ups.conf.next"
    mv "$NUT_CONFPATH/ups.conf.next" "$NUT_CONFPATH/ups.conf"
  }
  debug_changed() { [ "$(upsc good@127.0.0.1 driver.debug 2>/dev/null)" = 1 ]; }
  data_changed() { [ "$(upsc good@127.0.0.1 battery.runtime 2>/dev/null)" = 1800 ]; }
  initial_data_restored() { [ "$(upsc good@127.0.0.1 battery.runtime 2>/dev/null)" = 3600 ]; }
  write_reload_config dummy-ups "$NUT_CONFPATH/good.dev"
  await 'live debug-level reload' debug_changed
  same_good_pid
  test "$(worker_pid good)" = "$good_worker"
  write_reload_config dummy-ups "$NUT_CONFPATH/changed.dev"
  old_pid="$good_pid"
  await 'NUT-requested restart on port change' recovered
  await 'new port data after restart' data_changed
  test "$(cat "$(pid_file second)")" = "$second_pid"
  good_pid="$(cat "$(pid_file good)")"
  # Upstream resolves the PID using the NEW driver name. The supervisor must
  # reach the old owned process with NUT's reload-or-exit signal instead.
  test "$(worker_pid good)" = "$good_pid"
  write_reload_config apcupsd-ups "$NUT_CONFPATH/changed.dev"
  if upsdrvctl -c reload-or-exit good >/tmp/identity-reload.log 2>&1; then
    echo 'upstream driver identity behavior changed; review the migration gate' >&2
    exit 1
  fi
  old_driver_gone() { ! kill -0 "$good_pid" 2>/dev/null; }
  await 'renamed driver retires the old owned process' old_driver_gone
  test "$(cat "$(pid_file second)")" = "$second_pid"
  write_reload_config dummy-ups "$NUT_CONFPATH/changed.dev"
  await 'restored driver identity' data_changed
  write_config good
  await 'removed reload-isolation driver' removed
  await 'restored initial port' initial_data_restored
  echo 'NUT reload tests passed: live settings, port restart, driver identity replacement'
}
test_driver_reload

if [ "${NUT_READINESS_SAMPLES:-0}" -gt 0 ]; then
  sh /probe-stress.sh
fi

for crash in 1 2; do
  old_pid="$(cat "$(pid_file good)")"
  kill -KILL "$old_pid"
  await "driver recovery after crash $crash" recovered
done

kill -TERM "$supervisor_pid"
wait "$supervisor_pid"
supervisor_pid=
await 'supervisor termination cleans up NUT workers' workers_gone

# A stopped real driver cannot handle TERM. The supervisor must still reap it on time.
start_supervisor
await 'replacement driver writes a PID file' test -s "$(pid_file good)"
await 'replacement driver serves state' responsive good
stopped_pid="$(cat "$(pid_file good)")"
kill -STOP "$stopped_pid"
stop_started=$(date +%s)
kill -TERM "$supervisor_pid"
wait "$supervisor_pid"
supervisor_pid=
test "$(( $(date +%s) - stop_started ))" -le 8
await 'stopped driver is killed and reaped' workers_gone

kill -TERM "$server_pid"
wait "$server_pid"
server_pid=
echo 'NUT supervisor image tests passed: idle startup, partial failure, reload retry, PID preservation, crash recovery, termination'
