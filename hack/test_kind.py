"""Cluster-free tests for the Kind suite's ownership boundary."""

import importlib.util
import json
import os
from pathlib import Path
import select
import shutil
import signal
# Tests exercise owned local child-process cleanup.
import subprocess  # nosec B404
import sys
import tempfile
import time
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location("kind_runner", Path(__file__).with_name("test-kind.py"))
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)


class KindTest(unittest.TestCase):
    def assert_process_stopped(self, pid):
        stat = Path(f"/proc/{pid}/stat")
        deadline = time.monotonic() + 2
        while True:
            try:
                state = stat.read_text().split()[2]
            except (FileNotFoundError, ProcessLookupError):
                return
            if state in ("Z", "X"):
                return
            if time.monotonic() >= deadline:
                self.fail(f"owned descendant {pid} has not exited; last state was {state}")
            # Reaping the direct child does not synchronize descendant exit.
            time.sleep(0.01)

    def test_process_exit_observation_tolerates_concurrent_reaping(self):
        with patch.object(Path, "exists", return_value=True), \
                patch.object(Path, "read_text", side_effect=FileNotFoundError):
            self.assert_process_stopped(123)

    def test_process_exit_observation_waits_for_killed_descendant(self):
        with patch.object(Path, "exists", return_value=True), \
                patch.object(Path, "read_text", side_effect=["123 (python) R", "123 (python) Z"]), \
                patch.object(time, "sleep"):
            self.assert_process_stopped(123)

    def test_process_exit_observation_still_rejects_live_descendant(self):
        with patch.object(Path, "read_text", return_value="123 (python) R"), \
                patch.object(time, "monotonic", side_effect=[0, 0, 2]), \
                patch.object(time, "sleep"), \
                self.assertRaisesRegex(AssertionError, "owned descendant 123 has not exited"):
            self.assert_process_stopped(123)

    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name)
        self.state = self.root / "nut-kind-owned"
        self.state.mkdir(mode=0o700)
        self.normal = self.root / "user-config"
        self.normal.write_text("normal-context-must-stay-unchanged")
        self.env = dict(os.environ, KUBECONFIG=str(self.normal), KIND_CLUSTER="test",
                        NUT_OPERATOR_E2E_STARTUP="false")
        self.calls = []
        self.nodes = set()
        self.owned_id = "a" * 64
        self.replacement_id = "b" * 64
        self.failure = None
        self.cancel = False
        self.context = "kind-test-owned"
        self.uid = "owned-uid"

    def fake_run(self, args, env, timeout=30, capture=False, deadline=None):
        self.calls.append((args, dict(env)))
        phase = " ".join(args[:3])
        if args[:3] == ["kind", "get", "clusters"]:
            return "test\ntest-other"
        if args[:3] == ["kind", "create", "cluster"]:
            self.nodes = {self.owned_id}
            Path(env["KUBECONFIG"]).write_text("private-config")
        if args[:2] == ["docker", "ps"]:
            return "\n".join(sorted(self.nodes))
        if args[0] == "kubectl":
            return self.context if "current-context" in args else self.uid
        if args[:2] == ["docker", "rm"]:
            self.assertEqual(args[2:4], ["--force", "--volumes"])
            self.assertEqual(args[4:], [self.owned_id])
            missing = set(args[4:]) - self.nodes
            self.nodes.difference_update(args[4:])
            if missing:
                raise subprocess.CalledProcessError(1, args)
        if phase == self.failure:
            if self.cancel:
                raise KeyboardInterrupt("cancel fixture")
            raise subprocess.CalledProcessError(42, args)
        return ""

    def invoke(self, fake=None, args=None):
        with patch.object(runner.tempfile, "mkdtemp", return_value=str(self.state)), \
                patch.object(runner, "run", side_effect=fake or self.fake_run):
            if args is None:
                runner.suite(self.env)
            else:
                runner.main(args, self.env)

    def test_cli_default_is_unfiltered(self):
        self.env.update(GINKGO_FOCUS="not-a-cli-focus", GOFLAGS="-mod=readonly")
        original = dict(self.env)
        self.invoke(args=[])
        args, env = next(call for call in self.calls if call[0][0] == "go")
        self.assertEqual(args, ["go", "test", "-tags=e2e", "./test/e2e/", "-v",
                                "-ginkgo.v", "-timeout=60m"])
        self.assertEqual(env["GOFLAGS"], original["GOFLAGS"])
        self.assertEqual(self.env, original)
        self.assert_external_untouched()
        self.assertFalse(self.nodes)
        self.assertFalse(self.state.exists())

    def test_cli_focus_is_one_literal_argument_with_existing_boundaries(self):
        patterns = ["should run successfully|logical ShutdownFlow|NS-6",
                    r"$(touch forbidden); `echo x` & 'quoted' \"double\" \\ $HOME",
                    r"\p{L}+", "["]
        for focus in patterns:
            for startup, minutes in (("false", 60), ("true", 75)):
                with self.subTest(focus=focus, startup=startup):
                    self.state.mkdir(mode=0o700, exist_ok=True)
                    self.calls = []
                    self.env.update(NUT_OPERATOR_E2E_STARTUP=startup, GOFLAGS="-mod=readonly")
                    original = dict(self.env)
                    with patch("builtins.print") as log:
                        self.invoke(args=["--focus", focus])
                    args, env = next(call for call in self.calls if call[0][0] == "go")
                    self.assertEqual(args, ["go", "test", "-tags=e2e", "./test/e2e/", "-v",
                                            "-ginkgo.v", f"-timeout={minutes}m", "-ginkgo.focus", focus,
                                            "-ginkgo.fail-on-empty"])
                    self.assertEqual(env["GOFLAGS"], original["GOFLAGS"])
                    self.assertEqual(self.env, original)
                    self.assertEqual(self.calls[0][0], ["make", "--no-print-directory", "check-test-e2e-host"])
                    self.assertTrue(any("setup-test-e2e-cni" in args for args, _ in self.calls))
                    self.assertEqual(sum("current-context" in args for args, _ in self.calls), 2)
                    self.assertTrue(any("not full acceptance" in str(call) and repr(focus) in call.args[0]
                                        for call in log.call_args_list))
                    self.assert_external_untouched()
                    self.assertFalse(self.nodes)
                    self.assertFalse(self.state.exists())

    def test_cli_invalid_arguments_have_no_preflight_side_effects(self):
        invalid = [["--focus"], ["--focus", ""], ["--focus", " \t\n"],
                   ["--focus", "a\0b"], ["--focus=spec"], ["spec"],
                   ["--focus", "one", "two"], ["--focus", "one", "--focus", "two"],
                   ["verify", "--focus", "one"], ["--focus", "one", "verify"]]
        for args in invalid:
            with self.subTest(args=args), \
                    patch.object(runner, "run") as run, \
                    patch.object(runner, "suite") as suite, \
                    patch.object(runner, "verify") as verify, \
                    patch.object(runner.tempfile, "mkdtemp") as mkdtemp:
                with self.assertRaisesRegex(RuntimeError, "usage"):
                    runner.main(args, self.env)
                run.assert_not_called()
                suite.assert_not_called()
                verify.assert_not_called()
                mkdtemp.assert_not_called()

    def test_cli_verify_is_unchanged(self):
        with patch.object(runner, "verify") as verify, patch.object(runner, "suite") as suite:
            runner.main(["verify"], self.env)
            verify.assert_called_once_with(self.env)
            suite.assert_not_called()

    def assert_external_untouched(self):
        self.assertEqual(self.normal.read_text(), "normal-context-must-stay-unchanged")
        self.assertEqual(self.env["KUBECONFIG"], str(self.normal))
        for args, env in self.calls:
            if args[0] != "make" or "check-test-e2e-host" not in args:
                self.assertEqual(env["KUBECONFIG"], str(self.state / "kubeconfig"))
                self.assertEqual(env["KIND_CLUSTER"], "test-owned")
                self.assertEqual(env["KIND_EXPERIMENTAL_PROVIDER"], "docker")

    def test_success_private_configuration_and_cleanup(self):
        self.invoke()
        self.assert_external_untouched()
        self.assertFalse(self.state.exists())
        self.assertFalse(self.nodes)
        self.assertTrue(any(args[0] == "go" for args, _ in self.calls))

    def test_startup_profile_extends_only_the_owned_suite_budget(self):
        self.env["NUT_OPERATOR_E2E_STARTUP"] = "true"
        budgets = []

        def record(args, env, timeout=30, capture=False, deadline=None):
            budgets.append((args, timeout, deadline))
            return self.fake_run(args, env, timeout, capture, deadline)

        with patch.object(runner.time, "monotonic", return_value=100):
            self.invoke(record)
        go = next(item for item in budgets if item[0][0] == "go")
        self.assertIn("-timeout=75m", go[0])
        self.assertEqual(go[1:], (4530, 4900))
        deletion = next(item for item in budgets if item[0][:2] == ["docker", "rm"])
        self.assertEqual(deletion[1:], (90, 250))
        self.assert_external_untouched()
        self.assertFalse(self.nodes)

    def test_invalid_startup_profile_fails_before_cluster_creation(self):
        self.env["NUT_OPERATOR_E2E_STARTUP"] = "yes"
        with self.assertRaisesRegex(RuntimeError, "NUT_OPERATOR_E2E_STARTUP"):
            self.invoke()
        self.assertFalse(any(args[:2] == ["kind", "create"] for args, _ in self.calls))

    def test_failures_and_cancellation_clean_partial_resources(self):
        for phase in ("kind create cluster", "make --no-print-directory setup-test-e2e-cni", "go test -tags=e2e"):
            for cancel in (False, True):
                with self.subTest(phase=phase, cancel=cancel):
                    self.state.mkdir(mode=0o700, exist_ok=True)
                    self.calls = []
                    self.failure, self.cancel = phase, cancel
                    with self.assertRaises(KeyboardInterrupt if cancel else subprocess.CalledProcessError):
                        self.invoke()
                    self.assert_external_untouched()
                    self.assertFalse(self.nodes)
                    self.assertFalse(self.state.exists())

    def test_existing_cluster_is_never_reused_or_deleted(self):
        def existing(args, env, **kwargs):
            if args[:3] == ["kind", "get", "clusters"]:
                return "test-owned"
            return self.fake_run(args, env, **kwargs)
        with self.assertRaisesRegex(RuntimeError, "existing"):
            self.invoke(existing)
        self.assertFalse(any(args[1:3] == ["create", "cluster"] or args[:2] == ["docker", "rm"] for args, _ in self.calls))

    def test_context_mismatch_refuses_mutation(self):
        self.context = "production"
        with self.assertRaisesRegex(RuntimeError, "context"):
            self.invoke()
        self.assertFalse(any("setup-test-e2e-cni" in args or args[0] == "go" for args, _ in self.calls))
        self.assert_external_untouched()

    def test_uid_mismatch_refuses_suite(self):
        def changed(args, env, **kwargs):
            result = self.fake_run(args, env, **kwargs)
            if "setup-test-e2e-cni" in args:
                self.uid = "foreign-uid"
            return result
        with self.assertRaisesRegex(RuntimeError, "UID"):
            self.invoke(changed)
        self.assertFalse(any(args[0] == "go" for args, _ in self.calls))

    def test_replaced_containers_are_not_deleted(self):
        def changed(args, env, **kwargs):
            result = self.fake_run(args, env, **kwargs)
            if args[0] == "go":
                self.nodes = {self.replacement_id}
            return result
        with self.assertRaisesRegex(RuntimeError, "ownership changed"):
            self.invoke(changed)
        self.assertTrue(self.state.exists())
        self.assertFalse(any(args[:2] == ["docker", "rm"] for args, _ in self.calls))

    def test_replacement_after_verification_survives_and_reports_leftovers(self):
        def changed(args, env, **kwargs):
            if args[:2] == ["docker", "rm"]:
                # Verification has returned the original ID; the name now belongs
                # to a different container before Docker executes the removal.
                self.nodes = {self.replacement_id}
            return self.fake_run(args, env, **kwargs)
        with self.assertRaisesRegex(RuntimeError, "nodes remain"), \
                patch("sys.stderr") as stderr:
            self.invoke(changed)
        self.assertEqual(self.nodes, {self.replacement_id})
        self.assertTrue(self.state.exists())
        self.assertIn("nodes remain", "".join(call.args[0] for call in stderr.write.call_args_list))
        self.assertFalse(any(args[:2] == ["kind", "delete"] or args[:2] == ["docker", "network"]
                             for args, _ in self.calls))

    def test_cleanup_failure_is_not_a_pass(self):
        self.failure = "docker rm --force"
        with self.assertRaises(subprocess.CalledProcessError):
            self.invoke()
        self.assertTrue(self.state.exists())

    def test_cleanup_failure_preserves_original_test_error(self):
        def failing(args, env, **kwargs):
            if args[0] == "go":
                raise subprocess.CalledProcessError(17, args)
            if args[:2] == ["docker", "rm"]:
                raise subprocess.CalledProcessError(18, args)
            return self.fake_run(args, env, **kwargs)
        with self.assertRaises(subprocess.CalledProcessError) as error:
            self.invoke(failing)
        self.assertEqual(error.exception.returncode, 17)
        self.assertTrue(self.state.exists())

    def test_host_preflight_failure_creates_nothing(self):
        self.failure = "make --no-print-directory check-test-e2e-host"
        with patch.object(runner.tempfile, "mkdtemp") as mkdtemp:
            with patch.object(runner, "run", side_effect=self.fake_run):
                with self.assertRaises(subprocess.CalledProcessError):
                    runner.suite(self.env)
            mkdtemp.assert_not_called()
        self.assertEqual(len(self.calls), 1)

    def test_separate_runs_get_distinct_cluster_names(self):
        states = []
        def create_state(**_kwargs):
            state = self.root / ("nut-kind-" + str(len(states)))
            state.mkdir(mode=0o700)
            states.append(state)
            return str(state)
        names = []
        def record(args, env, **kwargs):
            if args[:3] == ["kind", "create", "cluster"]:
                names.append(env["KIND_CLUSTER"])
            if args[0] == "kubectl" and "current-context" in args:
                return "kind-" + env["KIND_CLUSTER"]
            return self.fake_run(args, env, **kwargs)
        with patch.object(runner.tempfile, "mkdtemp", side_effect=create_state), \
                patch.object(runner, "run", side_effect=record):
            runner.suite(self.env)
            runner.suite(self.env)
        self.assertEqual(len(set(names)), 2)
        self.assertTrue(all(not state.exists() for state in states))

    def test_verify_rejects_missing_owner_or_foreign_config(self):
        with self.assertRaises(RuntimeError):
            runner.verify({})
        (self.state / "owner.json").write_text(json.dumps({"cluster": "test-owned", "uid": "owned-uid"}))
        env = dict(self.env, KIND_E2E_STATE=str(self.state), KIND_CLUSTER="test-owned")
        with self.assertRaisesRegex(RuntimeError, "kubeconfig"):
            runner.verify(env)

    def test_verify_rejects_readable_or_symlinked_config(self):
        (self.state / "owner.json").write_text(json.dumps({"cluster": "test-owned", "uid": "owned-uid"}))
        config = self.state / "kubeconfig"
        config.write_text("private")
        config.chmod(0o644)
        env = dict(self.env, KIND_E2E_STATE=str(self.state), KIND_CLUSTER="test-owned", KUBECONFIG=str(config))
        with self.assertRaisesRegex(RuntimeError, "private"):
            runner.verify(env)
        config.unlink()
        config.symlink_to(self.normal)
        with self.assertRaisesRegex(RuntimeError, "private"):
            runner.verify(env)

    @unittest.skipUnless(shutil.which("make"), "GNU Make is required")
    def test_real_make_explicit_cluster_overrides_inherited_makeflags(self):
        makefile = self.root / "Makefile"
        makefile.write_text("probe:\n\t@echo $(KIND_CLUSTER)\n")
        env = dict(os.environ, MAKEFLAGS=" -- KIND_CLUSTER=foreign")
        result = runner.run(["make", "--no-print-directory", "-f", str(makefile),
                             "probe", "KIND_CLUSTER=owned"], env, capture=True)
        self.assertEqual(result, "owned")

    def test_command_deadline_caps_wait_and_refuses_expired_launch(self):
        process = Mock(returncode=0)
        process.communicate.return_value = ("", None)
        with patch.object(runner.time, "monotonic", return_value=100), \
                patch.object(runner.subprocess, "Popen", return_value=process) as launch:
            runner.run(["fixture"], self.env, timeout=30, deadline=103)
            process.communicate.assert_called_once_with(timeout=3)
            launch.reset_mock()
            with self.assertRaises(subprocess.TimeoutExpired):
                runner.run(["fixture"], self.env, deadline=100)
            launch.assert_not_called()

    def test_suite_deadline_caps_go_and_leaves_independent_cleanup_budget(self):
        real_run = runner.run
        now = [100.0]
        waits = []

        def bounded(args, env, timeout=30, capture=False, deadline=None):
            output = self.fake_run(args, env, timeout=timeout, capture=capture)
            process = Mock(returncode=0)
            duration = 0
            if args[:3] == ["kind", "create", "cluster"]:
                duration = 200
            elif "setup-test-e2e-cni" in args:
                duration = 800
            elif args[0] == "go":
                duration = 5000

            def communicate(timeout):
                nonlocal duration
                waits.append((args, timeout, deadline))
                elapsed, duration = duration, 0
                now[0] += min(elapsed, timeout)
                if elapsed > timeout:
                    raise subprocess.TimeoutExpired(args, timeout)
                return output, None

            process.communicate.side_effect = communicate
            with patch.object(runner.subprocess, "Popen", return_value=process):
                return real_run(args, env, timeout=timeout, capture=capture, deadline=deadline)

        with patch.object(runner.time, "monotonic", side_effect=lambda: now[0]), \
                patch.object(runner.os, "killpg") as killpg:
            with self.assertRaises(subprocess.TimeoutExpired) as error:
                self.invoke(bounded)
        self.assertEqual(error.exception.timeout, 2900)
        self.assertIn("-timeout=60m", error.exception.cmd)
        self.assertEqual(next(wait[1] for wait in waits if wait[0][:3] == ["kind", "create", "cluster"]), 240)
        self.assertEqual(next(wait[1] for wait in waits if "setup-test-e2e-cni" in wait[0]), 900)
        go_wait = next(wait for wait in waits if wait[0][0] == "go")
        self.assertEqual(go_wait[1:], (2900, 4000))
        deletion = next(wait for wait in waits if wait[0][:2] == ["docker", "rm"])
        self.assertEqual(deletion[1:], (90, 4150))
        self.assertEqual([call.args[1] for call in killpg.call_args_list], [signal.SIGTERM, signal.SIGKILL])
        self.assertFalse(self.nodes)
        self.assertFalse(self.state.exists())

    def test_repeated_cancellation_kills_and_reaps_before_failed_cluster_cleanup(self):
        real_run = runner.run
        events = []
        child_pid = None
        command = None
        child_code = (
            "import os,signal,time; signal.signal(signal.SIGTERM,signal.SIG_IGN); "
            "print(os.getpid(),flush=True); time.sleep(60)"
        )
        parent_code = (
            "import signal,subprocess,sys,time; "
            "signal.signal(signal.SIGTERM,signal.SIG_IGN); "
            "subprocess.Popen([sys.executable,'-c',sys.argv[1]]); time.sleep(60)"
        )

        def assert_stopped():
            self.assertIsNotNone(command.returncode, "command has not been reaped")
            self.assertEqual(command.returncode, -signal.SIGKILL)
            self.assert_process_stopped(child_pid)

        def dispatch(args, env, **kwargs):
            nonlocal command, child_pid
            if args[0] == "go":
                argv = [sys.executable, "-c", parent_code, child_code]
                # Fixed local programs; the session is owned exclusively by this test.
                command = subprocess.Popen(argv, env=env, start_new_session=True,  # nosec B603
                                           stdout=subprocess.PIPE, text=True)
                communicate = command.communicate

                def cancelled_communicate(timeout):
                    nonlocal child_pid
                    if not events:
                        self.assertTrue(select.select([command.stdout], [], [], 3)[0],
                                        "TERM-ignoring descendant did not start")
                        child_pid = int(command.stdout.readline())
                        events.append("cancel")
                        os.kill(os.getpid(), signal.SIGTERM)
                        self.fail("initial cancellation was ignored")
                    # Deliver both cancellations during the TERM wait and again
                    # during the final KILL/reap, while the real processes run.
                    events.append("teardown")
                    os.kill(os.getpid(), signal.SIGINT)
                    os.kill(os.getpid(), signal.SIGTERM)
                    result = communicate(timeout=timeout)
                    assert_stopped()
                    events.append("reaped")
                    return result

                with patch.object(runner.subprocess, "Popen", return_value=command), \
                        patch.object(command, "communicate", side_effect=cancelled_communicate):
                    try:
                        return real_run(argv, env, timeout=10, capture=True)
                    finally:
                        for sig in (signal.SIGINT, signal.SIGTERM):
                            self.assertIs(signal.getsignal(sig), runner.interrupted)
            if args[:2] == ["docker", "ps"] and command is not None:
                self.assertIn(events[-1], ("reaped", "cluster cleanup"))
                assert_stopped()
            if args[:2] == ["docker", "rm"]:
                self.assertEqual(events[-1], "reaped")
                assert_stopped()
                events.append("cluster cleanup")
                raise subprocess.CalledProcessError(18, args)
            return self.fake_run(args, env, **kwargs)

        previous = {sig: signal.signal(sig, runner.interrupted)
                    for sig in (signal.SIGINT, signal.SIGTERM)}
        try:
            with self.assertRaisesRegex(KeyboardInterrupt, f"received signal {signal.SIGTERM}"), \
                    patch("sys.stderr") as stderr:
                self.invoke(dispatch)
            teardown_output = "".join(call.args[0] for call in stderr.write.call_args_list)
            self.assertEqual(events, ["cancel", "teardown", "teardown", "reaped", "cluster cleanup"],
                             teardown_output)
            self.assertTrue(self.state.exists())
            self.assertIn("Kind cleanup failed", "".join(call.args[0] for call in stderr.write.call_args_list))
            for sig in previous:
                self.assertIs(signal.getsignal(sig), runner.interrupted)
        finally:
            for sig, handler in previous.items():
                signal.signal(sig, handler)
            if command is not None:
                try:
                    os.killpg(command.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                command.communicate(timeout=5)

    def test_timeout_kills_owned_descendant(self):
        pidfile = self.root / "child-pid"
        code = "import subprocess,time,sys; from pathlib import Path; p=subprocess.Popen([sys.executable,'-c','import time; time.sleep(60)']); Path(sys.argv[1]).write_text(str(p.pid)); time.sleep(60)"
        try:
            with self.assertRaises(subprocess.TimeoutExpired):
                runner.run([sys.executable, "-c", code, str(pidfile)], dict(os.environ), timeout=0.5, capture=True)
            pid = int(pidfile.read_text())
            self.assert_process_stopped(pid)
        finally:
            if pidfile.exists():
                try:
                    os.kill(int(pidfile.read_text()), signal.SIGKILL)
                except ProcessLookupError:
                    pass

    def test_sigterm_stops_command_group_before_returning(self):
        child_code = "import subprocess,time,sys; p=subprocess.Popen([sys.executable,'-c','import time; time.sleep(60)']); print(p.pid,flush=True); time.sleep(60)"
        driver_code = "import runpy,sys,os,signal; m=runpy.run_path(sys.argv[1]); signal.signal(signal.SIGTERM,m['interrupted']); m['run']([sys.executable,'-c',sys.argv[2]],dict(os.environ),timeout=20)"
        pid = None
        # Fixed test programs with no external input.
        with subprocess.Popen([sys.executable, "-c", driver_code, str(Path(runner.__file__).resolve()), child_code],  # nosec B603
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True) as process:
            try:
                self.assertTrue(select.select([process.stdout], [], [], 3)[0], "child did not start")
                pid = int(process.stdout.readline())
                process.terminate()
                process.communicate(timeout=12)
                self.assertNotEqual(process.returncode, 0)
                self.assert_process_stopped(pid)
            finally:
                if process.poll() is None:
                    process.kill()
                if pid is not None:
                    try:
                        os.kill(pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass


if __name__ == "__main__":
    unittest.main()
