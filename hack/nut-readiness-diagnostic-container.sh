#!/bin/sh
set -eu

# Invoked only inside the isolated supervisor fixture, with its single dummy UPS.
driver_pid=$(cat /run/nut/dummy-ups-good.pid)
cleanup() {
  rc=$?
  trap - EXIT
  kill -CONT "$driver_pid" 2>/dev/null || true
  exit "$rc"
}
trap cleanup EXIT
trap 'exit 143' TERM

responsive() {
  awk '$1 == "good" { for (i=1; i<=NF; i++) if ($i == "RESPONSIVE") ok=1 } END { exit !ok }' "$1"
}

upsdrvctl status good >/tmp/baseline-status.out 2>&1
responsive /tmp/baseline-status.out
test "$(upsc good@127.0.0.1 ups.status 2>/dev/null)" = OL
kill -STOP "$driver_pid"
# Wait for the kernel to confirm the driver is stopped, not merely signaled.
for attempt in $(seq 1 20); do
  grep -q '^State:.*T' "/proc/$driver_pid/status" && break
  sleep 0.1
done
grep -q '^State:.*T' "/proc/$driver_pid/status"

# upsd has its own cached state and freshness window. This is not a live-driver check.
test "$(upsc good@127.0.0.1 ups.status 2>/dev/null)" = OL
echo 'diagnostic: STOP-confirmed driver; upsd still returns OL'
bounded_rc=0
bounded_start=$(date +%s)
timeout 5 upsdrvctl status good >/tmp/bounded-status.out 2>&1 || bounded_rc=$?
case "$bounded_rc" in
  124|143) ;; # GNU timeout / the operand's BusyBox timeout.
  *) echo "unexpected bounded probe exit: $bounded_rc" >&2; exit 1 ;;
esac
test "$(( $(date +%s) - bounded_start ))" -ge 5
echo 'diagnostic: five-second probe deadline rejects the stopped driver'
probe_rc=0
timeout 12 upsdrvctl -DDDDD status good >/tmp/paused-status.out 2>&1 || probe_rc=$?
cat /tmp/paused-status.out
test "$probe_rc" -eq 0
grep -q '^State:.*T' "/proc/$driver_pid/status"
awk '$1 == "good" && $6 == "N/A" { found=1 } END { exit !found }' /tmp/paused-status.out
if responsive /tmp/paused-status.out; then
  echo 'diagnostic: upstream false-positive RESPONSIVE with missing socket PID'
else
  awk '$1 == "good" && $5 == "NOT_RESPONSIVE" { found=1 } END { exit !found }' /tmp/paused-status.out
  echo 'diagnostic: upstream correctly reports NOT_RESPONSIVE; review pinned-version evidence'
fi
kill -CONT "$driver_pid"
timeout 12 upsdrvctl status good >/tmp/resumed-status.out 2>&1
cat /tmp/resumed-status.out
responsive /tmp/resumed-status.out
awk -v driver_pid="$driver_pid" '$1 == "good" && $6 == driver_pid { found=1 } END { exit !found }' /tmp/resumed-status.out
test "$(cat /run/nut/dummy-ups-good.pid)" = "$driver_pid"
test "$(upsc good@127.0.0.1 ups.status 2>/dev/null)" = OL
echo 'diagnostic passed: cached reads do not prove driver liveness; same PID recovered'
