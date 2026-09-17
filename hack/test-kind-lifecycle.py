#!/usr/bin/env python3
"""Opt-in LIVE cancellation qualification; invokes the shipped runner unchanged.

Run from the checkout with pinned Kind/tools already provisioned and the ordinary
host preflight satisfied. No installs or sysctl writes are performed. Each case
has a 300s observation budget plus 180s for runner cleanup. Failure retains private
logs/state (possibly credentials); this observer never deletes Docker resources.
This supplements, and does not replace, the normal suite or exact-image gate.
"""

import importlib.util
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess  # nosec B404
import sys
import tempfile
import time

ROOT = Path(__file__).resolve().parents[1]
spec = importlib.util.spec_from_file_location("owned_kind", ROOT / "hack/test-kind.py")
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)
OBSERVE_SECONDS = 300
CLEANUP_SECONDS = 180
PREFIX = "nut-lifecycle"


def all_ids(env, deadline):
    return set(runner.run(["docker", "ps", "-aq", "--no-trunc"], env,
                          capture=True, timeout=5, deadline=deadline).split())


def snapshot(paths):
    # Byte equality also preserves current-context, without loading user credentials.
    return {path: (path.read_bytes() if path.exists() else None) for path in paths}


def assert_unchanged(before):
    if snapshot(before) != before:
        raise RuntimeError("external kubeconfig/context changed")


def assert_cancelled(log):
    output = log.read_text()
    if f"Kind suite failed: received signal {signal.SIGTERM}" not in output:
        raise RuntimeError("runner did not acknowledge SIGTERM")
    if "Kind cleanup failed" in output or "Command teardown failed" in output:
        raise RuntimeError("runner reported cleanup failure")


def cluster_name(state):
    return (PREFIX + "-" + state.name.removeprefix("nut-kind-")).replace("_", "-")


def observe(tmp, env, baseline, captured, deadline, cleanup=False):
    states = list(tmp.glob("nut-kind-*"))
    if not states:
        return None, None
    if len(states) != 1 or not states[0].is_dir() or states[0].is_symlink():
        raise RuntimeError("ambiguous runner state")
    state = states[0]
    cluster = cluster_name(state)
    private = dict(env, KIND_CLUSTER=cluster, KIND_E2E_STATE=str(state),
                   KUBECONFIG=str(state / "kubeconfig"))
    ids = runner.node_ids(private, deadline=deadline)
    if ids & baseline:
        raise RuntimeError("observed pre-existing container; refusing qualification")
    captured.update(ids)
    if cleanup:
        return state, None
    owner = state / "owner.json"
    try:
        record = json.loads(owner.read_text())
    except FileNotFoundError:
        record = None
    except json.JSONDecodeError:
        # The runner writes this file directly; a concurrent read can see a prefix.
        return state, None
    if record is None:
        return state, "partial" if ids else None
    if record.get("cluster") != cluster or not record.get("uid"):
        raise RuntimeError("invalid API owner record")
    if ids:
        runner.verify(private, deadline=deadline)
        return state, "api"
    return state, None


def qualify(process, tmp, env, phase, baseline, captured):
    deadline = time.monotonic() + OBSERVE_SECONDS
    state = None
    while True:
        if process.poll() is not None:
            raise RuntimeError("runner exited before cancellation milestone")
        if time.monotonic() >= deadline:
            raise RuntimeError("cancellation milestone deadline expired")
        state, milestone = observe(tmp, env, baseline, captured, deadline)
        if milestone == phase:
            if process.poll() is not None:
                raise RuntimeError("runner lost at cancellation milestone")
            # A partial-startup observation must still precede the owner record.
            if phase == "partial" and (state / "owner.json").exists():
                raise RuntimeError("partial-startup window missed")
            # Keep the label identity even if cancellation removes all private state.
            cluster_env = dict(env, KIND_CLUSTER=cluster_name(state))
            process.send_signal(signal.SIGTERM)
            print(f"{phase}: observed {len(captured)} node IDs; sent SIGTERM", flush=True)
            break
        if phase == "partial" and milestone == "api":
            raise RuntimeError("partial-startup window missed")
        time.sleep(0.1)

    cleanup_deadline = time.monotonic() + CLEANUP_SECONDS
    while process.poll() is None:
        if time.monotonic() >= cleanup_deadline:
            raise RuntimeError("runner cleanup deadline expired")
        # Capture nodes created while the Kind command is being stopped as well.
        observe(tmp, env, baseline, captured, cleanup_deadline, cleanup=True)
        time.sleep(0.1)
    # The shipped runner catches SIGTERM and reports failure as exit 1. Raw
    # signal death, success, and other exit statuses do not prove its cleanup.
    if process.returncode != 1:
        raise RuntimeError(f"unexpected cancellation exit {process.returncode}")
    if not captured:
        raise RuntimeError("no owned node IDs observed")
    remaining = all_ids(env, cleanup_deadline)
    missing = baseline - remaining
    if missing:
        raise RuntimeError(f"pre-existing Docker IDs disappeared: {sorted(missing)}")
    labeled = runner.node_ids(cluster_env, deadline=cleanup_deadline)
    survivors = (captured & remaining) | labeled
    if survivors:
        raise RuntimeError(f"captured or cluster-labeled node IDs survived: {sorted(survivors)}")
    if state.exists() or list(tmp.glob("nut-kind-*")):
        raise RuntimeError("runner retained private state after cancellation")


def stop_failed_child(process):
    # Repeated cancellation must not interrupt either the TERM grace period or
    # the final KILL/reap. Restore the caller's handlers even when reaping fails.
    previous = {sig: signal.signal(sig, signal.SIG_IGN)
                for sig in (signal.SIGINT, signal.SIGTERM)}
    try:
        if process.poll() is None:
            process.send_signal(signal.SIGTERM)
            try:
                process.wait(timeout=CLEANUP_SECONDS)
            except subprocess.TimeoutExpired:
                process.kill()
                process.wait(timeout=5)
                raise RuntimeError("runner required SIGKILL during failure cleanup")
    finally:
        for sig, handler in previous.items():
            signal.signal(sig, handler)


def rehearsal(phase, base_env):
    root = Path(tempfile.mkdtemp(prefix="nut-lifecycle-"))
    tmp = root / "tmp"
    tmp.mkdir(mode=0o700)
    sentinel = root / "external-kubeconfig"
    sentinel.write_text('apiVersion: v1\nkind: Config\ncurrent-context: sentinel\n'
                        'contexts:\n- name: sentinel\n  context: {cluster: sentinel}\n'
                        'clusters:\n- name: sentinel\n  cluster: {server: "https://127.0.0.1:1"}\n')
    sentinel.chmod(0o600)
    paths = {Path.home() / ".kube/config", sentinel}
    paths.update(Path(p).absolute() for p in base_env.get("KUBECONFIG", "").split(os.pathsep) if p)
    before = snapshot(paths)
    env = dict(base_env, TMPDIR=str(tmp), KUBECONFIG=str(sentinel),
               KIND_CLUSTER=PREFIX, KIND_CONFIG=str(ROOT / "test/e2e/kind-config.yaml"),
               KIND_EXPERIMENTAL_PROVIDER="docker", KUBECTL_KUBERC="false")
    # Do not inherit another make invocation's variable overrides/identity.
    for key in ("MAKEFLAGS", "MFLAGS", "MAKEOVERRIDES", "KIND_E2E_STATE"):
        env.pop(key, None)
    process = None
    captured = set()
    success = False
    try:
        baseline = all_ids(env, time.monotonic() + 30)
        with (root / "runner.log").open("w") as log:
            process = subprocess.Popen(  # nosec B603
                [sys.executable, "-B", str(ROOT / "hack/test-kind.py")],
                cwd=ROOT, env=env, stdout=log, stderr=subprocess.STDOUT,
                start_new_session=True)
            try:
                qualify(process, tmp, env, phase, baseline, captured)
            finally:
                stop_failed_child(process)
        assert_cancelled(root / "runner.log")
        assert_unchanged(before)
        success = True
    finally:
        if success:
            shutil.rmtree(root)
        else:
            print(f"FAILED {phase}: private evidence retained at {root}; "
                  f"observed IDs: {sorted(captured)}", file=sys.stderr)
            assert_unchanged(before)
    print(f"PASS {phase}: cancelled runner cleaned all observed IDs; kubeconfigs unchanged")


def main():
    if sys.argv[1:]:
        raise RuntimeError("usage: python3 -B hack/test-kind-lifecycle.py")
    os.chdir(ROOT)
    env = dict(os.environ)
    for key in ("MAKEFLAGS", "MFLAGS", "MAKEOVERRIDES", "E2E_MIN_INOTIFY_INSTANCES"):
        env.pop(key, None)
    runner.run(["make", "--no-print-directory", "check-test-e2e-host"], env)
    for phase in ("partial", "api"):
        rehearsal(phase, env)


if __name__ == "__main__":
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, runner.interrupted)
    try:
        main()
    except (Exception, KeyboardInterrupt) as error:
        print(f"Lifecycle qualification failed: {error}", file=sys.stderr)
        sys.exit(1)
