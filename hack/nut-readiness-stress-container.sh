#!/bin/sh
set -eu

# The enclosing fixture provides real upsd + dummy-ups. No shutdown command can act on a host.
printf '%s\n' \
  'MONITOR good@127.0.0.1 1 test component-test-only secondary' \
  'MINSUPPLIES 1' 'POLLFREQ 1' 'POLLFREQALERT 1' \
  'SHUTDOWNCMD "/bin/false"' 'POWERDOWNFLAG /tmp/test-powerdown' \
  > "$NUT_CONFPATH/upsmon.conf"
upsmon -F -p >/tmp/upsmon.log 2>&1 &
monitor_pid=$!
cleanup() {
  rc=$?
  trap - EXIT
  kill "$monitor_pid" 2>/dev/null || true
  wait "$monitor_pid" 2>/dev/null || true
  cat /tmp/upsmon.log
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 143' TERM

probe_misses=0
server_misses=0
disagreements=0
for sample in $(seq 1 "$NUT_READINESS_SAMPLES"); do
  kill -0 "$monitor_pid"
  pids=
  for client in 1 2 3 4; do
    upsc good@127.0.0.1 ups.status > "/tmp/client-$client.out" 2>/dev/null &
    pids="$pids $!"
  done
  probe_ok=1
  timeout -k 1 5 /usr/local/bin/nut-driver-ready > /tmp/driver-status.out 2>&1 || probe_ok=0
  server_ok=1
  for pid in $pids; do wait "$pid" || server_ok=0; done
  for client in 1 2 3 4; do
    [ "$(cat "/tmp/client-$client.out")" = OL ] || server_ok=0
  done
  if [ "$probe_ok" -eq 0 ]; then
    probe_misses=$((probe_misses + 1))
    cat /tmp/driver-status.out
    if [ "$server_ok" -eq 1 ]; then disagreements=$((disagreements + 1)); fi
  fi
  if [ "$server_ok" -eq 0 ]; then server_misses=$((server_misses + 1)); fi
  printf 'readiness sample=%s probe_ok=%s server_ok=%s\n' "$sample" "$probe_ok" "$server_ok"
  sleep 1
done
printf 'readiness samples=%s probe_misses=%s server_misses=%s disagreements=%s\n' \
  "$NUT_READINESS_SAMPLES" "$probe_misses" "$server_misses" "$disagreements"
test "$probe_misses" -eq 0
test "$server_misses" -eq 0
if ! grep -q 'logged into UPS.*good' /tmp/upsd.log; then
  echo 'upsmon never authenticated to the test UPS' >&2
  cat /tmp/upsd.log >&2
  exit 1
fi
