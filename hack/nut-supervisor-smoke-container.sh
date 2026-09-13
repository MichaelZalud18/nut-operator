#!/bin/sh
set -eu

export NUT_CONFPATH=/tmp/nut
export NUT_SUPERVISOR_INTERVAL_SECONDS=1
export NUT_SUPERVISOR_STATE_DIR=/run/nut/driver-supervisor
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
removed() { [ ! -e "$NUT_SUPERVISOR_STATE_DIR/second.pid" ] && ! responsive second; }
recovered() {
  [ -s "$(pid_file good)" ] &&
    [ "$(cat "$(pid_file good)")" != "$old_pid" ] && responsive good
}
workers_gone() { ! pidof dummy-ups upsdrvctl >/dev/null; }
reload_retried() { [ "$(grep -c 'upsd reload failed, will retry' /tmp/supervisor.log)" -ge 2 ]; }

printf 'ups.status: OL\nbattery.runtime: 3600\nbattery.charge: 100\n' > "$NUT_CONFPATH/good.dev"
printf 'LISTEN 127.0.0.1 3493\nALLOW_NO_DEVICE true\n' > "$NUT_CONFPATH/upsd.conf"
printf '[test]\n  password = component-test-only\n  upsmon secondary\n' > "$NUT_CONFPATH/upsd.users"
write_config
upsd -FF >/tmp/upsd.log 2>&1 &
server_pid=$!
sh /supervisor.sh >/tmp/supervisor.log 2>&1 &
supervisor_pid=$!
await 'idle upsd startup' test -s /run/nut/upsd.pid
kill -0 "$supervisor_pid"

write_config good bad
await 'healthy driver beside invalid definition' responsive good
await 'failed driver is retried' grep -q 'bad exited with status' /tmp/supervisor.log
good_pid="$(cat "$(pid_file good)")"
good_worker="$(cat "$NUT_SUPERVISOR_STATE_DIR/good.pid")"
# Make the real reload command fail without killing the server or replacing any NUT binary.
mv /run/nut/upsd.pid /run/nut/upsd.pid.saved
write_config good second bad
await 'failed upsd reload is retried' reload_retried
same_good_pid
test ! -e "$NUT_SUPERVISOR_STATE_DIR/second.pid"
mv /run/nut/upsd.pid.saved /run/nut/upsd.pid
await 'new driver becomes visible through upsd reload' responsive second
same_good_pid
test "$(cat "$NUT_SUPERVISOR_STATE_DIR/good.pid")" = "$good_worker"
write_config good
await 'removed driver disappears from upsd and supervisor' removed
same_good_pid
test "$(cat "$NUT_SUPERVISOR_STATE_DIR/good.pid")" = "$good_worker"

# NUT reports a malformed section as "no UPS definitions", just like an empty file.
printf '[unterminated\n' > "$NUT_CONFPATH/ups.conf.next"
mv "$NUT_CONFPATH/ups.conf.next" "$NUT_CONFPATH/ups.conf"
await 'malformed configuration is refused' grep -q 'keeping existing workers' /tmp/supervisor.log
same_good_pid
responsive good
test "$(cat "$NUT_SUPERVISOR_STATE_DIR/good.pid")" = "$good_worker"
write_config good

for crash in 1 2; do
  old_pid="$(cat "$(pid_file good)")"
  kill -KILL "$old_pid"
  await "driver recovery after crash $crash" recovered
done

kill -TERM "$supervisor_pid"
wait "$supervisor_pid"
supervisor_pid=
await 'supervisor termination cleans up NUT workers' workers_gone
kill -TERM "$server_pid"
wait "$server_pid"
server_pid=
echo 'NUT supervisor image tests passed: idle startup, partial failure, reload retry, PID preservation, crash recovery, termination'
