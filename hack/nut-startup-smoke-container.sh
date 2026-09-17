#!/bin/sh
set -eu

export NUT_CONFPATH=/tmp/nut-startup
export NUT_QUIET_INIT_BANNER=true
seconds=${NUT_STARTUP_SECONDS:-660}
case "$seconds" in ''|*[!0-9]*) exit 64 ;; esac
test "$seconds" -ge 1 && test "$seconds" -le 660
output=/tmp/observation
mkdir -p "$output" "$NUT_CONFPATH"
server_pid=
supervisor_pid=
monitor_pids=
started=
samples=0
probe_errors=0
startup_probe_errors=0
ready_probe_errors=0
watchdog_errors=0
first_ready=-1
driver_pid_changes=0
driver_absent_samples=0
process_error_samples=0
last_driver_identity=
authenticated_clients=0
reconnect_logins=0
restarted_client=0
restart_elapsed=-1
original_client_pid=
replacement_client_pid=

monotonic_seconds() { cut -d. -f1 /proc/uptime; }
alive() {
  [ -r "/proc/$1/stat" ] && kill -0 "$1" 2>/dev/null &&
    ! grep -q '^State:.*Z' "/proc/$1/status"
}
logins() {
  awk -v client="observer$1@" 'index($0, client) && /logged into UPS/ { n++ } END { print n+0 }' "$output/upsd.log"
}
evidence_failed() {
  cleanup_evidence_errors=$((cleanup_evidence_errors + 1))
  rc=1
  printf 'startup cleanup evidence failure: %s\n' "$1" >&2
}
cleanup() {
  rc=$?
  trap - EXIT
  trap '' INT TERM
  set +e
  cleanup_evidence_errors=0
  elapsed=0
  [ -z "$started" ] || elapsed=$(( $(monotonic_seconds) - started ))
  full_window=0
  if [ "$seconds" -eq 660 ] && [ "$elapsed" -ge 660 ]; then full_window=1; fi
  # Snapshot before intentional termination so cleanup is not counted as instability.
  authenticated_clients=0
  reconnect_logins=0
  printf 'client\tpid\talive\tauthenticated_logins\trepeat_logins\tcommok_events\tcommbad_events\tnocomm_events\n' >"$output/clients.tsv" || evidence_failed 'write client summary header'
  client=0
  for pid in $monitor_pids; do
    client=$((client + 1))
    count=$(logins "$client") || { count=0; evidence_failed "read client $client logins"; }
    repeats=0
    if [ "$count" -gt 0 ]; then
      authenticated_clients=$((authenticated_clients + 1))
      repeats=$((count - 1))
      reconnect_logins=$((reconnect_logins + repeats))
    fi
    running=0
    alive "$pid" && running=1
    events=$(awk -F '\t' '$2 == "COMMOK" { ok++ } $2 == "COMMBAD" { bad++ } $2 == "NOCOMM" { missing++ }
      END { printf "%d\t%d\t%d", ok, bad, missing }' "$output/client-$client-events.tsv") || { events=unknown; evidence_failed "read client $client events"; }
    printf '%s\t%s\t%s\t%s\t%s\t%s\n' "$client" "$pid" "$running" "$count" "$repeats" "$events" >>"$output/clients.tsv" || evidence_failed "write client $client summary"
  done
  driver_exits=$(awk '/Driver exited/ { n++ } END { print n+0 }' "$output/supervisor.log") || { driver_exits=0; evidence_failed 'read driver exits'; }
  if [ "$rc" -eq 0 ] && { [ "$authenticated_clients" -ne 8 ] || [ "$driver_exits" -gt 0 ]; }; then rc=1; fi
  if [ "$rc" -eq 0 ] && [ "$seconds" -gt 30 ]; then
    if [ "$restarted_client" -ne 1 ] || [ "$(logins 1)" -lt 2 ]; then rc=1; fi
  fi
  printf 'requested_seconds\t%s\nobserved_seconds\t%s\nfull_eleven_minute_window\t%s\nsamples\t%s\nprobe_errors\t%s\nstartup_probe_errors\t%s\nafter_first_ready_probe_errors\t%s\nprobe_watchdog_errors\t%s\nfirst_ready_seconds\t%s\ndriver_pid_changes\t%s\ndriver_absent_samples\t%s\nsupervisor_reported_driver_exits\t%s\nprocess_error_samples\t%s\nauthenticated_clients\t%s\nrepeat_authenticated_logins\t%s\nexit_code\t%s\n' \
    "$seconds" "$elapsed" "$full_window" "$samples" "$probe_errors" "$startup_probe_errors" "$ready_probe_errors" "$watchdog_errors" "$first_ready" \
    "$driver_pid_changes" "$driver_absent_samples" "$driver_exits" "$process_error_samples" "$authenticated_clients" "$reconnect_logins" "$rc" >"$output/summary.tsv" || evidence_failed 'write observation summary'
  printf 'intentional_client_restarts\t%s\nclient_restart_elapsed_seconds\t%s\noriginal_client_1_pid\t%s\nreplacement_client_1_pid\t%s\n' \
    "$restarted_client" "$restart_elapsed" "$original_client_pid" "$replacement_client_pid" >>"$output/summary.tsv" || evidence_failed 'write reconnect summary'
  printf 'cleanup_evidence_errors\t%s\n' "$cleanup_evidence_errors" >>"$output/summary.tsv" || evidence_failed 'record evidence errors'
  for pid in $monitor_pids; do kill -TERM "$pid" 2>/dev/null || true; done
  [ -z "$supervisor_pid" ] || kill -TERM "$supervisor_pid" 2>/dev/null || true
  [ -z "$server_pid" ] || kill -TERM "$server_pid" 2>/dev/null || true
  for pid in $monitor_pids $supervisor_pid $server_pid; do wait "$pid" 2>/dev/null || true; done
  cat "$output/summary.tsv" || evidence_failed 'display observation summary'
  exit "$rc"
}
: >"$output/upsd.log"
: >"$output/supervisor.log"
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

command -v nut-driver-ready
command -v nut-driver-supervisor
command -v upsd
command -v upsmon
printf '[good]\n driver = dummy-ups\n port = %s/good.dev\n' "$NUT_CONFPATH" >"$NUT_CONFPATH/ups.conf"
printf 'ups.status: OL\nbattery.runtime: 3600\nbattery.charge: 100\n' >"$NUT_CONFPATH/good.dev"
printf 'LISTEN 127.0.0.1 3493\nALLOW_NO_DEVICE true\n' >"$NUT_CONFPATH/upsd.conf"
: >"$NUT_CONFPATH/upsd.users"
cat > /tmp/startup-notify.sh <<'NOTIFY'
#!/bin/sh
printf '%s\t%s\t%s\n' "$(cut -d' ' -f1 /proc/uptime)" "${NOTIFYTYPE:-UNKNOWN}" "${UPSNAME:-UNKNOWN}" >>"$NUT_STARTUP_EVENTS"
NOTIFY
chmod 0555 /tmp/startup-notify.sh
for client in $(seq 1 8); do
  printf '[observer%s]\n password = startup-fixture-only\n upsmon secondary\n' "$client" >>"$NUT_CONFPATH/upsd.users"
  client_dir="/tmp/client-$client"
  mkdir -p "$client_dir"
  printf 'MONITOR good@127.0.0.1 1 observer%s startup-fixture-only secondary\n' "$client" >"$client_dir/upsmon.conf"
  printf '%s\n' 'MINSUPPLIES 1' 'POLLFREQ 1' 'POLLFREQALERT 1' 'DEADTIME 15' \
    'SHUTDOWNCMD "/bin/false"' "POWERDOWNFLAG $client_dir/powerdown" \
    'NOTIFYCMD /tmp/startup-notify.sh' 'NOTIFYFLAG COMMOK EXEC' 'NOTIFYFLAG COMMBAD EXEC' \
    'NOTIFYFLAG NOCOMM EXEC' >>"$client_dir/upsmon.conf"
  : >"$output/client-$client-events.tsv"
done
printf 'elapsed_seconds\tprobe_rc\tprobe_seconds\tdriver_pid\tdriver_start_ticks\tdriver_alive\tsupervisor_alive\tupsd_alive\tclients_alive\tauthenticated_clients\tlogin_events\n' >"$output/samples.tsv"

# Start all consumers with the first server launch; do not prewarm the driver or
# wait for readiness/authentication before recording observations.
started=$(monotonic_seconds)
upsd -FF >"$output/upsd.log" 2>&1 &
server_pid=$!
/usr/local/bin/nut-driver-supervisor >"$output/supervisor.log" 2>&1 &
supervisor_pid=$!
for client in $(seq 1 8); do
  NUT_CONFPATH="/tmp/client-$client" NUT_ALTPIDPATH="/tmp/client-$client" \
    NUT_STARTUP_EVENTS="$output/client-$client-events.tsv" \
    upsmon -F -p >"$output/client-$client.log" 2>&1 &
  monitor_pids="$monitor_pids $!"
done

while [ "$(( $(monotonic_seconds) - started ))" -lt "$seconds" ]; do
  sample_started=$(monotonic_seconds)
  elapsed=$((sample_started - started))
  if [ "$elapsed" -ge 30 ] && [ "$restarted_client" -eq 0 ]; then
    remaining_pids=
    for pid in $monitor_pids; do
      if [ -z "$original_client_pid" ]; then original_client_pid=$pid;
      else remaining_pids="$remaining_pids $pid"; fi
    done
    printf 'elapsed_seconds=%s action=restart client=1 old_pid=%s\n' "$elapsed" "$original_client_pid" >>"$output/reconnect.log"
    kill -TERM "$original_client_pid"
    wait "$original_client_pid" || true
    NUT_CONFPATH=/tmp/client-1 NUT_ALTPIDPATH=/tmp/client-1 \
      NUT_STARTUP_EVENTS="$output/client-1-events.tsv" \
      upsmon -F -p >>"$output/client-1.log" 2>&1 &
    replacement_client_pid=$!
    monitor_pids="$replacement_client_pid$remaining_pids"
    restarted_client=1
    restart_elapsed=$(( $(monotonic_seconds) - started ))
    printf 'elapsed_seconds=%s action=started client=1 new_pid=%s\n' "$restart_elapsed" "$replacement_client_pid" >>"$output/reconnect.log"
  fi
  samples=$((samples + 1))
  probe_rc=0
  timeout -k 1 5 /usr/local/bin/nut-driver-ready >"$output/probe-$samples.log" 2>&1 || probe_rc=$?
  probe_seconds=$(( $(monotonic_seconds) - sample_started ))
  if [ "$probe_rc" -eq 0 ]; then
    [ "$first_ready" -ge 0 ] || first_ready=$(( $(monotonic_seconds) - started ))
  else
    probe_errors=$((probe_errors + 1))
    if [ "$first_ready" -lt 0 ]; then startup_probe_errors=$((startup_probe_errors + 1));
    else ready_probe_errors=$((ready_probe_errors + 1)); fi
    if [ "$probe_rc" -ne 1 ]; then watchdog_errors=$((watchdog_errors + 1)); fi
  fi
  driver_pid=$(cat /run/nut/dummy-ups-good.pid 2>/dev/null || true)
  driver_ticks=missing
  driver_alive=0
  if [ -n "$driver_pid" ] && alive "$driver_pid"; then
    driver_alive=1
    driver_ticks=$(awk '{ print $22 }' "/proc/$driver_pid/stat" 2>/dev/null || true)
    identity="$driver_pid:$driver_ticks"
    if [ -n "$last_driver_identity" ] && [ "$identity" != "$last_driver_identity" ]; then driver_pid_changes=$((driver_pid_changes + 1)); fi
    last_driver_identity=$identity
  else driver_absent_samples=$((driver_absent_samples + 1)); fi
  supervisor_alive=0
  server_alive=0
  alive "$supervisor_pid" && supervisor_alive=1
  alive "$server_pid" && server_alive=1
  clients_alive=0
  for pid in $monitor_pids; do
    if alive "$pid"; then clients_alive=$((clients_alive + 1)); fi
  done
  if [ "$supervisor_alive" -ne 1 ] || [ "$server_alive" -ne 1 ] || [ "$clients_alive" -ne 8 ]; then process_error_samples=$((process_error_samples + 1)); fi
  authenticated_clients=0
  login_events=0
  for client in $(seq 1 8); do
    count=$(logins "$client")
    if [ "$count" -gt 0 ]; then authenticated_clients=$((authenticated_clients + 1)); fi
    login_events=$((login_events + count))
  done
  printf '%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n' "$elapsed" "$probe_rc" "$probe_seconds" "${driver_pid:-missing}" "$driver_ticks" "$driver_alive" "$supervisor_alive" "$server_alive" "$clients_alive" "$authenticated_clients" "$login_events" >>"$output/samples.tsv"
  sleep 1
done

test "$first_ready" -ge 0
test "$ready_probe_errors" -eq 0
test "$watchdog_errors" -eq 0
test "$driver_pid_changes" -eq 0
test "$process_error_samples" -eq 0
