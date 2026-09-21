#!/usr/bin/env python3
"""Own one disposable Kind suite run; never load the user's kubeconfig."""

import json
import os
from pathlib import Path
import re
import shutil
import signal
# This harness owns bounded local tool processes.
import subprocess  # nosec B404
import sys
import tempfile
import time


def run(args, env, timeout=30, capture=False, deadline=None):
    if deadline is not None:
        remaining = deadline - time.monotonic()
        if remaining <= 0:
            raise subprocess.TimeoutExpired(args, 0)
        timeout = min(timeout, remaining)
    # Argument vectors come from local harness/config, never a shell or cluster data.
    process = subprocess.Popen(args, env=env, start_new_session=True,  # nosec B603
                               stdout=subprocess.PIPE if capture else None, text=True)
    try:
        output, _ = process.communicate(timeout=timeout)
        if process.returncode:
            raise subprocess.CalledProcessError(process.returncode, args)
        return output.strip() if capture else ""
    except BaseException:
        # Stop and reap the whole owned command group before cluster cleanup.
        previous = {sig: signal.signal(sig, signal.SIG_IGN) for sig in (signal.SIGINT, signal.SIGTERM)}
        try:
            try:
                os.killpg(process.pid, signal.SIGTERM)
            except ProcessLookupError:
                pass
            try:
                process.communicate(timeout=5)
            except subprocess.TimeoutExpired:
                pass
            finally:
                try:
                    os.killpg(process.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                process.communicate(timeout=5)
        except BaseException as teardown_error:
            print(f"Command teardown failed: {teardown_error}", file=sys.stderr)
        finally:
            for sig, handler in previous.items():
                signal.signal(sig, handler)
        raise


def kubectl(env, *args, deadline=None):
    return run(["kubectl", "--request-timeout=15s", *args], env, capture=True, deadline=deadline)


def verify(env, deadline=None):
    state = Path(env.get("KIND_E2E_STATE", ""))
    if not state.is_absolute() or not state.is_dir():
        raise RuntimeError("use make test-e2e: no private Kind run state")
    owner = json.loads((state / "owner.json").read_text())
    config = state / "kubeconfig"
    if env.get("KUBECONFIG") != str(config) or env.get("KIND_CLUSTER") != owner["cluster"]:
        raise RuntimeError("Kind run identity or kubeconfig does not match its owner")
    if config.is_symlink() or not config.is_file() or config.stat().st_mode & 0o077:
        raise RuntimeError("Kind kubeconfig must be a private regular file")
    if kubectl(env, "config", "current-context", deadline=deadline) != "kind-" + owner["cluster"]:
        raise RuntimeError("refusing mismatched Kind context")
    uid = kubectl(env, "get", "namespace", "kube-system", "-o", "jsonpath={.metadata.uid}", deadline=deadline)
    if not owner.get("uid") or uid != owner["uid"]:
        raise RuntimeError("refusing mismatched Kind cluster UID")


def node_ids(env, deadline=None):
    ids = run(["docker", "ps", "-aq", "--no-trunc", "--filter",
               "label=io.x-k8s.kind.cluster=" + env["KIND_CLUSTER"]], env, capture=True, deadline=deadline)
    return set(ids.split())


def suite(env, focus=None):
    startup = env.get("NUT_OPERATOR_E2E_STARTUP", "false")
    if startup not in ("true", "false", ""):
        raise RuntimeError("NUT_OPERATOR_E2E_STARTUP must be true or false")
    # The expanded suite reached its NetworkPolicy specs at 45 minutes in CI
    # (OM-3); reserve time for those specs and teardown after the EX-34 scenarios.
    # The optional eleven-minute observation retains its additional allowance.
    test_minutes = 75 if startup == "true" else 60
    # Preserve the host guardrail before creating any cluster or changing tracked files.
    run(["make", "--no-print-directory", "check-test-e2e-host"], env)
    prefix = env.get("KIND_CLUSTER", "nut-operator-test-e2e")
    if not re.fullmatch(r"[a-z0-9][a-z0-9-]{0,39}", prefix):
        raise RuntimeError("KIND_CLUSTER must be a short lowercase cluster-name prefix")
    state = Path(tempfile.mkdtemp(prefix="nut-kind-"))
    owned = False
    nodes = None
    cleanup_ok = True
    run_env = dict(env, KIND_E2E_STATE=str(state), KUBECONFIG=str(state / "kubeconfig"),
                   KIND_CLUSTER=prefix + "-" + state.name.removeprefix("nut-kind-"),
                   KUBECTL_KUBERC="false", KIND_EXPERIMENTAL_PROVIDER="docker")
    # tempfile suffixes may contain underscores; Kind names may not.
    run_env["KIND_CLUSTER"] = run_env["KIND_CLUSTER"].replace("_", "-")
    kind = run_env.get("KIND", "kind")
    deadline = time.monotonic() + (test_minutes + 5) * 60
    try:
        clusters = run([kind, "get", "clusters"], run_env, capture=True, deadline=deadline).splitlines()
        if run_env["KIND_CLUSTER"] in clusters:
            raise RuntimeError("refusing to reuse an existing Kind cluster")
        (state / "kubeconfig").touch(mode=0o600)
        owned = True
        try:
            run([kind, "create", "cluster", "--name", run_env["KIND_CLUSTER"],
                 "--config", run_env.get("KIND_CONFIG", "test/e2e/kind-config.yaml"),
                 "--kubeconfig", run_env["KUBECONFIG"], "--wait", "120s"], run_env, timeout=240, deadline=deadline)
        finally:
            # Ownership evidence is required for cleanup even after the run expires.
            nodes = node_ids(run_env)
        if not nodes:
            raise RuntimeError("Kind created no owned node containers")
        os.chmod(run_env["KUBECONFIG"], 0o600)
        uid = kubectl(run_env, "get", "namespace", "kube-system", "-o", "jsonpath={.metadata.uid}", deadline=deadline)
        (state / "owner.json").write_text(json.dumps({"cluster": run_env["KIND_CLUSTER"], "uid": uid}))
        verify(run_env, deadline=deadline)
        run(["make", "--no-print-directory", "setup-test-e2e-cni",
             "KIND_CLUSTER=" + run_env["KIND_CLUSTER"]], run_env, timeout=900, deadline=deadline)
        verify(run_env, deadline=deadline)
        test_args = ["go", "test", "-tags=e2e", "./test/e2e/", "-v", "-ginkgo.v", f"-timeout={test_minutes}m"]
        if focus is not None:
            test_args.extend(["-ginkgo.focus", focus, "-ginkgo.fail-on-empty"])
        run(test_args, run_env, timeout=test_minutes * 60 + 30, deadline=deadline)
    finally:
        primary_error = sys.exc_info()[1]
        # A second cancellation must not interrupt bounded cleanup.
        previous = {sig: signal.signal(sig, signal.SIG_IGN) for sig in (signal.SIGINT, signal.SIGTERM)}
        cleanup_deadline = time.monotonic() + 150
        try:
            if owned:
                current = node_ids(run_env, deadline=cleanup_deadline)
                if nodes is None or not current.issubset(nodes):
                    raise RuntimeError("refusing cleanup: Kind node ownership changed")
                try:
                    if current:
                        run(["docker", "rm", "--force", "--volumes", *sorted(current)], run_env,
                            timeout=90, deadline=cleanup_deadline)
                finally:
                    if node_ids(run_env, deadline=cleanup_deadline):
                        raise RuntimeError("Kind nodes remain after cleanup")
        except BaseException as cleanup_error:
            cleanup_ok = False
            print(f"Kind cleanup failed ({cleanup_error}); private state retained at {state}", file=sys.stderr)
            if primary_error is None:
                raise
        finally:
            for sig, handler in previous.items():
                signal.signal(sig, handler)
            if cleanup_ok:
                shutil.rmtree(state)


def interrupted(signum, _frame):
    raise KeyboardInterrupt(f"received signal {signum}")


def main(args, env):
    if args == ["verify"]:
        verify(env)
    elif not args:
        suite(env)
    elif len(args) == 2 and args[0] == "--focus" and args[1].strip() and "\0" not in args[1]:
        # Validate only CLI shape; Ginkgo uses Go regexp semantics, not Python's.
        print(f"Focused Kind scope: {args[1]!r}; local iteration, not full acceptance", flush=True)
        suite(env, focus=args[1])
    else:
        raise RuntimeError("usage: test-kind.py [verify | --focus <nonempty-ginkgo-regexp>]")


if __name__ == "__main__":
    for sig in (signal.SIGINT, signal.SIGTERM):
        signal.signal(sig, interrupted)
    try:
        main(sys.argv[1:], os.environ)
    except (Exception, KeyboardInterrupt) as error:
        print(f"Kind suite failed: {error}", file=sys.stderr)
        sys.exit(1)
