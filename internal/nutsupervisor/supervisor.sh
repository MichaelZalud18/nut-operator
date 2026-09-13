#!/bin/sh
set -u
state_dir="${NUT_SUPERVISOR_STATE_DIR:-/run/nut/driver-supervisor}"
config_dir="${NUT_CONFPATH:-/etc/nut}"
interval="${NUT_SUPERVISOR_INTERVAL_SECONDS:-5}"
case "$interval" in
  ""|*[!0-9]*|0) echo "driver-supervisor: interval must be a positive integer" >&2; exit 1 ;;
esac
case "$interval" in
  *[1-9]*) ;;
  *) echo "driver-supervisor: interval must be positive" >&2; exit 1 ;;
esac
export NUT_CONFPATH="$config_dir"
mkdir -p "$state_dir" || exit 1

driverPidFile() {
  printf '%s/%s.pid\n' "$state_dir" "$1"
}

driverExitFile() {
  printf '%s/%s.exit\n' "$state_dir" "$1"
}

driverDigestFile() {
  printf '%s/%s.digest\n' "$state_dir" "$1"
}

deviceConfigDigest() {
  ups="$1"
  awk -v name="$ups" '
    $0 == "[" name "]" { insection = 1; next }
    /^\[/ { insection = 0 }
    insection { print }
  ' "$config_dir/ups.conf" 2>/dev/null | md5sum | cut -d' ' -f1
}

configDigest() {
  cat "$@" 2>/dev/null | md5sum | cut -d' ' -f1
}

configuredDrivers() {
  list_error="$state_dir/list.err"
  output="$(NUT_QUIET_INIT_BANNER=true timeout -s KILL 5 upsdrvctl list 2>"$list_error")"
  rc="$?"
  if [ "$rc" -eq 0 ]; then
    printf '%s\n' "$output"
    return 0
  fi
  # The renderer emits a zero-byte file for zero devices. NUT also uses this
  # diagnostic for malformed section headers, which must not remove live workers.
  if [ -f "$config_dir/ups.conf" ] && [ ! -s "$config_dir/ups.conf" ] && grep -q "no UPS definitions found" "$list_error"; then
    return 0
  fi
  echo "driver-supervisor: cannot list drivers from $config_dir/ups.conf; keeping existing workers" >&2
  cat "$list_error" >&2
  return 1
}

driverRunning() {
  ups="$1"
  pid_file="$(driverPidFile "$ups")"
  exit_file="$(driverExitFile "$ups")"
  if [ ! -s "$pid_file" ] || [ -e "$exit_file" ]; then
    return 1
  fi
  pid="$(cat "$pid_file" 2>/dev/null || true)"
  [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null
}

reapDriver() {
  ups="$1"
  pid_file="$(driverPidFile "$ups")"
  exit_file="$(driverExitFile "$ups")"
  if [ -s "$pid_file" ]; then
    pid="$(cat "$pid_file" 2>/dev/null || true)"
    if [ -n "$pid" ]; then
      wait "$pid" 2>/dev/null || true
    fi
  fi
  rm -f "$pid_file" "$exit_file"
}

startDriver() {
  ups="$1"
  pid_file="$(driverPidFile "$ups")"
  exit_file="$(driverExitFile "$ups")"
  rm -f "$exit_file"
  echo "driver-supervisor: starting $ups"
  (
    stopChild() {
      trap '' INT TERM
      if [ -n "${driver_child:-}" ]; then
        kill -TERM "$driver_child" 2>/dev/null || true
        remaining=5
        while kill -0 "$driver_child" 2>/dev/null && [ "$remaining" -gt 0 ]; do
          sleep 1
          remaining=$((remaining - 1))
        done
        if kill -0 "$driver_child" 2>/dev/null; then
          echo "driver-supervisor: $ups ignored termination; killing owned child"
          kill -KILL "$driver_child" 2>/dev/null || true
        fi
        wait "$driver_child" 2>/dev/null || true
      fi
      exit 0
    }
    trap stopChild INT TERM
    NUT_QUIET_INIT_BANNER=true upsdrvctl -FF start "$ups" &
    driver_child="$!"
    wait "$driver_child"
    rc="$?"
    echo "$rc" > "$exit_file"
    exit "$rc"
  ) &
  echo "$!" > "$pid_file"
}

stopDriver() {
  ups="$1"
  pid_file="$(driverPidFile "$ups")"
  if [ -s "$pid_file" ]; then
    pid="$(cat "$pid_file" 2>/dev/null || true)"
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
      echo "driver-supervisor: stopping $ups"
      kill -TERM "$pid" 2>/dev/null || true
      wait "$pid" 2>/dev/null || true
    fi
  fi
  rm -f "$pid_file" "$(driverExitFile "$ups")"
  NUT_QUIET_INIT_BANNER=true timeout -s KILL 5 upsdrvctl stop "$ups" >/dev/null 2>&1 || true
}

stopAllDrivers() {
  trap '' INT TERM
  if [ -n "${sleep_pid:-}" ]; then
    kill "$sleep_pid" 2>/dev/null || true
    wait "$sleep_pid" 2>/dev/null || true
  fi
  # Start every worker's grace period together, then reap our own children.
  # Their foreground NUT processes are terminated by stopChild, not by a global kill.
  for pid_file in "$state_dir"/*.pid; do
    [ -s "$pid_file" ] || continue
    pid="$(cat "$pid_file")"
    kill -TERM "$pid" 2>/dev/null || true
  done
  for pid_file in "$state_dir"/*.pid; do
    [ -e "$pid_file" ] || continue
    ups="${pid_file##*/}"
    ups="${ups%.pid}"
    reapDriver "$ups"
  done
}

driverInList() {
  printf '%s\n' "$2" | grep -qx "$1"
}

reconcileDrivers() {
  configured="$(configuredDrivers)" || return 1

  for pid_file in "$state_dir"/*.pid; do
    [ -e "$pid_file" ] || continue
    ups="${pid_file##*/}"
    ups="${ups%.pid}"
    if ! driverInList "$ups" "$configured"; then
      stopDriver "$ups"
      rm -f "$(driverDigestFile "$ups")"
    fi
  done

  for ups in $configured; do
    digest="$(deviceConfigDigest "$ups")"
    digest_file="$(driverDigestFile "$ups")"
    last_digest="$(cat "$digest_file" 2>/dev/null || true)"

    if driverRunning "$ups" && [ "$digest" = "$last_digest" ]; then
      continue
    fi

    if driverRunning "$ups"; then
      echo "driver-supervisor: $ups configuration changed; restarting"
      stopDriver "$ups"
    elif [ -s "$(driverPidFile "$ups")" ]; then
      if [ -s "$(driverExitFile "$ups")" ]; then
        rc="$(cat "$(driverExitFile "$ups")" 2>/dev/null || true)"
        echo "driver-supervisor: $ups exited with status ${rc:-unknown}; restarting"
      else
        echo "driver-supervisor: $ups is not running; restarting"
      fi
      reapDriver "$ups"
    fi

    startDriver "$ups"
    printf '%s\n' "$digest" > "$digest_file"
  done
}

trap 'stopAllDrivers; exit 0' INT TERM

rm -f "$state_dir"/*.pid "$state_dir"/*.exit "$state_dir"/*.digest 2>/dev/null || true
last_server_digest="$(configDigest "$config_dir/ups.conf" "$config_dir/upsd.users")"
last_driver_digest="$(configDigest "$config_dir/ups.conf")"
reconcileDrivers || true

while true; do
  sleep "$interval" &
  sleep_pid=$!
  wait "$sleep_pid" || true
  sleep_pid=

  server_reload_ok=true
  current_server_digest="$(configDigest "$config_dir/ups.conf" "$config_dir/upsd.users")"
  if [ "$current_server_digest" != "$last_server_digest" ]; then
    # Validate enumeration before upsd can discard its last working device set.
    configuredDrivers >/dev/null || continue
    echo "driver-supervisor: reloadable server configuration changed, reloading upsd"
    if timeout -s KILL 5 upsd -c reload; then
      last_server_digest="$current_server_digest"
    else
      server_reload_ok=false
      echo "driver-supervisor: upsd reload failed, will retry"
    fi
  fi

  current_driver_digest="$(configDigest "$config_dir/ups.conf")"
  if [ "$current_driver_digest" != "$last_driver_digest" ]; then
    echo "driver-supervisor: driver configuration changed, reconciling managed drivers"
    if [ "$server_reload_ok" = "true" ] && reconcileDrivers; then
      last_driver_digest="$current_driver_digest"
    else
      echo "driver-supervisor: driver configuration reconcile failed, will retry"
    fi
  else
    reconcileDrivers || true
  fi
done
