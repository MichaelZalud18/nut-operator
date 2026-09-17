"""Cluster-free component tests; these are not LIVE qualification evidence."""

import importlib.util
import json
import os
from pathlib import Path
import signal
import subprocess  # nosec B404
import tempfile
import unittest
from unittest.mock import Mock, patch

spec = importlib.util.spec_from_file_location("lifecycle", Path(__file__).with_name("test-kind-lifecycle.py"))
lifecycle = importlib.util.module_from_spec(spec)
spec.loader.exec_module(lifecycle)


class LifecycleTest(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.tmp = Path(self.temp.name)
        self.state = self.tmp / "nut-kind-owned"
        self.state.mkdir()
        self.captured = set()

    def test_partial_requires_new_real_node_observation(self):
        with patch.object(lifecycle.runner, "node_ids", side_effect=[set(), {"new"}]), \
                patch.object(lifecycle.runner, "verify") as verify:
            self.assertEqual(lifecycle.observe(self.tmp, {}, {"foreign"}, self.captured, 100)[1], None)
            self.assertEqual(lifecycle.observe(self.tmp, {}, {"foreign"}, self.captured, 100)[1], "partial")
            self.assertEqual(self.captured, {"new"})
            verify.assert_not_called()

    def test_existing_id_is_never_owned(self):
        with patch.object(lifecycle.runner, "node_ids", return_value={"foreign"}), \
                self.assertRaisesRegex(RuntimeError, "pre-existing"):
            lifecycle.observe(self.tmp, {}, {"foreign"}, self.captured, 100)
        self.assertFalse(self.captured)

    def test_api_requires_matching_owner_and_live_verification(self):
        owner = self.state / "owner.json"
        owner.write_text(json.dumps({"cluster": "nut-lifecycle-owned", "uid": "uid"}))
        with patch.object(lifecycle.runner, "node_ids", return_value={"new"}), \
                patch.object(lifecycle.runner, "verify") as verify:
            self.assertEqual(lifecycle.observe(self.tmp, {}, set(), self.captured, 100)[1], "api")
            env = verify.call_args.args[0]
            self.assertEqual(env["KUBECONFIG"], str(self.state / "kubeconfig"))
            verify.side_effect = RuntimeError("wrong UID")
            with self.assertRaisesRegex(RuntimeError, "wrong UID"):
                lifecycle.observe(self.tmp, {}, set(), self.captured, 100)
            # Teardown captures late nodes without depending on a live API.
            lifecycle.observe(self.tmp, {}, set(), self.captured, 100, cleanup=True)

    def test_incomplete_owner_is_not_partial_milestone(self):
        (self.state / "owner.json").write_text('{"cluster":')
        with patch.object(lifecycle.runner, "node_ids", return_value={"new"}):
            self.assertIsNone(lifecycle.observe(self.tmp, {}, set(), self.captured, 100)[1])

    def run_qualification(self, phase="partial", exit_code=1, survivors=(), retained=False,
                          baseline=(), labeled=(), label_effect=None):
        process = Mock(returncode=exit_code)
        process.poll.side_effect = [None, None, exit_code]
        def observed(*args, **kwargs):
            self.captured.add("new")
            if not retained:
                self.state.rmdir()
            return self.state, phase
        with patch.object(lifecycle, "observe", side_effect=observed), \
                patch.object(lifecycle, "all_ids", return_value=set(survivors)), \
                patch.object(lifecycle.runner, "node_ids", return_value=set(labeled),
                             side_effect=label_effect):
            lifecycle.qualify(process, self.tmp, {}, phase, set(baseline), self.captured)
        process.send_signal.assert_called_once_with(signal.SIGTERM)

    def test_success_both_milestones(self):
        for phase in ("partial", "api"):
            with self.subTest(phase=phase):
                self.state.mkdir(exist_ok=True)
                self.run_qualification(phase)

    def test_raw_signal_death_and_success_exit_fail(self):
        for code in (-signal.SIGTERM, -signal.SIGKILL, 0):
            with self.subTest(code=code), self.assertRaisesRegex(RuntimeError, "unexpected cancellation exit"):
                self.state.mkdir(exist_ok=True)
                self.run_qualification(exit_code=code)

    def test_surviving_ids_fail_even_after_relabel(self):
        with self.assertRaisesRegex(RuntimeError, "survived"):
            self.run_qualification(survivors={"new", "foreign"})

    def test_late_labeled_node_fails_after_state_deleted(self):
        self.state = self.state.rename(self.tmp / "nut-kind-owned_suffix")
        for phase in ("partial", "api"):
            with self.subTest(phase=phase):
                self.state.mkdir(exist_ok=True)

                def final_nodes(env, *, deadline):
                    self.assertFalse(self.state.exists())
                    self.assertEqual(env["KIND_CLUSTER"], "nut-lifecycle-owned-suffix")
                    self.assertGreater(deadline, lifecycle.time.monotonic())
                    return {"late-node"}

                with self.assertRaisesRegex(RuntimeError, "survived.*late-node"):
                    # The late node appears after even the all-container snapshot.
                    self.run_qualification(phase, baseline={"foreign"}, survivors={"foreign"},
                                           label_effect=final_nodes)

    def test_final_label_query_failure_cannot_pass(self):
        with self.assertRaisesRegex(RuntimeError, "Docker unavailable"):
            self.run_qualification(label_effect=RuntimeError("Docker unavailable"))

    def test_unrelated_new_container_does_not_fail(self):
        self.run_qualification(baseline={"foreign"}, survivors={"foreign", "unrelated-new"})

    def test_pre_existing_ids_must_remain(self):
        self.run_qualification(baseline={"foreign"}, survivors={"foreign"})

    def test_missing_baseline_fails_even_when_owned_ids_gone(self):
        with self.assertRaisesRegex(RuntimeError, "pre-existing Docker IDs disappeared"):
            self.run_qualification(baseline={"foreign"}, survivors={"replacement"})

    def test_failure_cleanup_ignores_repeated_signals_and_restores_handlers(self):
        for force_kill in (False, True):
            with self.subTest(force_kill=force_kill):
                process = Mock()
                process.poll.return_value = None
                waits = []

                def wait(timeout):
                    waits.append(timeout)
                    os.kill(os.getpid(), signal.SIGINT)
                    os.kill(os.getpid(), signal.SIGTERM)
                    if force_kill and len(waits) == 1:
                        raise subprocess.TimeoutExpired("runner", timeout)
                    process.returncode = -signal.SIGKILL if force_kill else 1
                    return process.returncode

                process.wait.side_effect = wait
                previous = {sig: signal.signal(sig, lifecycle.runner.interrupted)
                            for sig in (signal.SIGINT, signal.SIGTERM)}
                try:
                    if force_kill:
                        with self.assertRaisesRegex(RuntimeError, "required SIGKILL"):
                            lifecycle.stop_failed_child(process)
                        process.kill.assert_called_once_with()
                        self.assertEqual(waits, [lifecycle.CLEANUP_SECONDS, 5])
                    else:
                        lifecycle.stop_failed_child(process)
                        process.kill.assert_not_called()
                        self.assertEqual(waits, [lifecycle.CLEANUP_SECONDS])
                    process.send_signal.assert_called_once_with(signal.SIGTERM)
                    for sig in previous:
                        self.assertIs(signal.getsignal(sig), lifecycle.runner.interrupted)
                finally:
                    for sig, handler in previous.items():
                        signal.signal(sig, handler)

    def test_failed_final_reap_is_bounded_and_restores_handlers(self):
        process = Mock()
        process.poll.return_value = None
        process.wait.side_effect = subprocess.TimeoutExpired("runner", 5)
        previous = {sig: signal.getsignal(sig) for sig in (signal.SIGINT, signal.SIGTERM)}
        with self.assertRaises(subprocess.TimeoutExpired):
            lifecycle.stop_failed_child(process)
        self.assertEqual([call.kwargs["timeout"] for call in process.wait.call_args_list],
                         [lifecycle.CLEANUP_SECONDS, 5])
        process.kill.assert_called_once_with()
        for sig, handler in previous.items():
            self.assertIs(signal.getsignal(sig), handler)

    def test_cleanup_failure_retains_private_evidence(self):
        root = self.tmp / "evidence"
        root.mkdir()
        with patch.object(lifecycle.tempfile, "mkdtemp", return_value=str(root)), \
                patch.object(lifecycle, "all_ids", return_value=set()), \
                patch.object(lifecycle.subprocess, "Popen"), \
                patch.object(lifecycle, "qualify", side_effect=RuntimeError("qualification failed")), \
                patch.object(lifecycle, "stop_failed_child", side_effect=RuntimeError("reap failed")), \
                patch.object(lifecycle.sys, "stderr"), \
                self.assertRaisesRegex(RuntimeError, "reap failed"):
            lifecycle.rehearsal("partial", {})
        self.assertTrue((root / "runner.log").is_file())
        self.assertTrue((root / "external-kubeconfig").is_file())
        self.assertTrue((root / "tmp").is_dir())

    def test_retained_state_fails(self):
        with self.assertRaisesRegex(RuntimeError, "retained private state"):
            self.run_qualification(retained=True)

    def test_early_exit_never_sends_qualification_signal(self):
        process = Mock()
        process.poll.return_value = 1
        with self.assertRaisesRegex(RuntimeError, "before cancellation"):
            lifecycle.qualify(process, self.tmp, {}, "partial", set(), set())
        process.send_signal.assert_not_called()

    def test_observation_deadline_fails_without_signal(self):
        process = Mock()
        process.poll.return_value = None
        with patch.object(lifecycle.time, "monotonic", side_effect=[0, 301]), \
                self.assertRaisesRegex(RuntimeError, "milestone deadline"):
            lifecycle.qualify(process, self.tmp, {}, "partial", set(), set())
        process.send_signal.assert_not_called()

    def test_cleanup_deadline_is_failure(self):
        process = Mock()
        process.poll.return_value = None
        with patch.object(lifecycle, "observe", return_value=(self.state, "partial")), \
                patch.object(lifecycle.time, "monotonic", side_effect=[0, 1, 2, 183]), \
                self.assertRaisesRegex(RuntimeError, "cleanup deadline"):
            lifecycle.qualify(process, self.tmp, {}, "partial", set(), {"new"})
        process.send_signal.assert_called_once_with(signal.SIGTERM)

    def test_external_context_mutation_detected(self):
        config = self.tmp / "external"
        config.write_text("current-context: original")
        before = lifecycle.snapshot([config])
        lifecycle.assert_unchanged(before)
        config.write_text("current-context: changed")
        with self.assertRaisesRegex(RuntimeError, "context changed"):
            lifecycle.assert_unchanged(before)

    def test_cancellation_diagnostic_required_and_cleanup_errors_rejected(self):
        log = self.tmp / "runner.log"
        log.write_text("Kind suite failed: unrelated failure\n")
        with self.assertRaisesRegex(RuntimeError, "acknowledge SIGTERM"):
            lifecycle.assert_cancelled(log)
        log.write_text(f"Kind suite failed: received signal {signal.SIGTERM}\n")
        lifecycle.assert_cancelled(log)
        for diagnostic in ("Kind cleanup failed", "Command teardown failed"):
            log.write_text(f"{diagnostic}\nKind suite failed: received signal {signal.SIGTERM}\n")
            with self.assertRaisesRegex(RuntimeError, "cleanup failure"):
                lifecycle.assert_cancelled(log)

    def test_missed_partial_window_does_not_cancel_as_success(self):
        process = Mock()
        process.poll.return_value = None
        with patch.object(lifecycle, "observe", return_value=(self.state, "api")), \
                self.assertRaisesRegex(RuntimeError, "window missed"):
            lifecycle.qualify(process, self.tmp, {}, "partial", set(), {"new"})
        process.send_signal.assert_not_called()

    def test_host_preflight_failure_prevents_rehearsal(self):
        with patch.object(lifecycle.sys, "argv", ["test-kind-lifecycle.py"]), \
                patch.object(lifecycle.os, "chdir"), \
                patch.object(lifecycle.runner, "run", side_effect=RuntimeError("host preflight")), \
                patch.object(lifecycle, "rehearsal") as rehearsal, \
                self.assertRaisesRegex(RuntimeError, "host preflight"):
            lifecycle.main()
        rehearsal.assert_not_called()


if __name__ == "__main__":
    unittest.main()
