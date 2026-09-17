"""Docker-free ownership, timeout, cancellation and diagnostic regression tests."""
import importlib.util
import json
import os
from pathlib import Path
import signal
# Local harness regression tests.
import subprocess  # nosec B404
import sys
import tempfile
import unittest
import uuid
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("netbox_harness", Path(__file__).with_name("test-netbox.py"))
harness = importlib.util.module_from_spec(spec)
spec.loader.exec_module(harness)


class FakeDocker:
    def __init__(self):
        self.calls = []
        self.objects = {}
        self.fail_remove = None
        self.timeout_create = False

    def __call__(self, args, **kwargs):
        self.calls.append((args, kwargs))
        result = subprocess.CompletedProcess(args, 0, "", "")
        if args[1:3] == ["network", "create"] or args[1] == "create":
            name = args[-1] if args[1] == "network" else args[args.index("--name")+1]
            label = args[args.index("--label")+1].split("=",1)
            identity = uuid.uuid4().hex + uuid.uuid4().hex
            self.objects[name] = {"Id":identity, "Labels":dict([label]), "Config":{"Labels":dict([label])}}
            result.stdout = identity
            if self.timeout_create:
                raise harness.HarnessError("command deadline exceeded")
        elif args[2:3] == ["inspect"]:
            obj = next((obj for name,obj in self.objects.items() if args[-1] in (name,obj["Id"])),None)
            if obj is None:
                result.returncode, result.stderr = 1, "Error: No such object"
            else:
                result.stdout = json.dumps([obj])
        elif args[2:3] == ["rm"]:
            name = next(name for name,obj in self.objects.items() if args[-1]==obj["Id"])
            if name == self.fail_remove:
                raise harness.HarnessError("command deadline exceeded")
            del self.objects[name]
        return result


class ResourcesTest(unittest.TestCase):
    def test_owned_reverse_cleanup_no_host_ports_and_idempotence(self):
        docker = FakeDocker()
        resources = harness.Resources(docker)
        resources.create_network()
        for role in harness.IMAGES:
            resources.create_container(role)
        resources.cleanup()
        resources.cleanup()
        self.assertEqual({}, docker.objects)
        removals = [args for args, _ in docker.calls if args[2:3] == ["rm"]]
        self.assertEqual(4, len(removals))
        self.assertEqual("network", removals[-1][1])
        self.assertTrue(all("--volumes" in args for args in removals[:-1]))
        for args, kwargs in docker.calls:
            self.assertFalse(set(args) & {"-p", "-P", "--publish", "--publish-all", "--privileged", "--pid=host"})
            if args[2:3] in (["rm"], ["inspect"]):
                self.assertEqual(20, kwargs["timeout"])
        self.assertIn("--internal", docker.calls[0][0])

    def test_ambiguous_create_timeout_still_cleans_created_network(self):
        docker = FakeDocker()
        docker.timeout_create = True
        resources = harness.Resources(docker)
        with self.assertRaises(harness.HarnessError):
            resources.create_network()
        resources.cleanup()
        self.assertFalse(docker.objects)

    def test_refuses_unowned_resource_but_continues_cleanup(self):
        docker = FakeDocker()
        resources = harness.Resources(docker)
        resources.create_network()
        name = resources.create_container("netbox")
        docker.objects[name]["Config"]["Labels"] = {harness.LABEL:"another-run"}
        with self.assertRaisesRegex(harness.HarnessError,"cleanup incomplete"):
            resources.cleanup()
        self.assertIn(name,docker.objects)
        self.assertNotIn(resources.network,docker.objects)

    def test_cleanup_timeout_does_not_skip_other_resources(self):
        docker = FakeDocker()
        resources = harness.Resources(docker)
        resources.create_network()
        docker.fail_remove = resources.create_container("netbox")
        with self.assertRaises(harness.HarnessError):
            resources.cleanup()
        self.assertNotIn(resources.network,docker.objects)

    def test_absent_resource_is_safe_and_owners_are_unique(self):
        docker = FakeDocker()
        first, second = harness.Resources(docker), harness.Resources(docker)
        self.assertNotEqual(first.owner,second.owner)
        first.resources.append(("container",first.owner+"-missing",""))
        first.cleanup()


class CommandTest(unittest.TestCase):
    def test_failure_does_not_expose_child_command_or_output(self):
        # Synthetic marker tests redaction; it is not an authentication credential.
        secret = "synthetic-secret-never-print"  # nosec B105
        with self.assertRaises(harness.HarnessError) as caught:
            harness.command([sys.executable,"-c","import sys; print(sys.argv[1]); sys.exit(1)",secret])
        self.assertNotIn(secret,str(caught.exception))

    def test_timeout_kills_child_process_group(self):
        with tempfile.TemporaryDirectory() as directory:
            marker = Path(directory)/"pid"
            script = "import os, pathlib, time; pathlib.Path(os.sys.argv[1]).write_text(str(os.getpid())); time.sleep(60)"
            with self.assertRaisesRegex(harness.HarnessError,"deadline exceeded"):
                harness.command([sys.executable,"-c",script,str(marker)],timeout=0.3)
            with self.assertRaises(ProcessLookupError):
                os.kill(int(marker.read_text()),0)

    def test_cancellation_also_terminates_group(self):
        with patch.object(harness.subprocess,"Popen") as popen, patch.object(harness.os,"killpg") as kill, \
                patch.object(harness.signal,"signal",return_value=signal.SIG_DFL) as handlers:
            child = popen.return_value
            child.pid = 12345
            child.communicate.side_effect = [harness.HarnessError("cancelled"),("","")]
            with self.assertRaisesRegex(harness.HarnessError,"cancelled"):
                harness.command(["docker","info"])
            kill.assert_called_once_with(12345,signal.SIGKILL)
            self.assertTrue(popen.call_args.kwargs["start_new_session"])
            for sig in (signal.SIGINT,signal.SIGTERM,signal.SIGALRM):
                self.assertIn(unittest.mock.call(sig,signal.SIG_IGN),handlers.call_args_list)
                self.assertIn(unittest.mock.call(sig,signal.SIG_DFL),handlers.call_args_list)


class LifecycleTest(unittest.TestCase):
    def test_success_failure_and_signal_always_cleanup(self):
        for error in (None, harness.HarnessError("fixture failed"), harness.HarnessError("interrupted by signal 15")):
            with self.subTest(error=error), patch.object(harness,"Resources") as resources, \
                    patch.object(harness,"run_suite",side_effect=error), \
                    patch.object(harness.signal,"signal"), patch.object(harness.signal,"alarm") as alarm, \
                    patch("builtins.print"):
                self.assertEqual(int(error is not None),harness.main())
                resources.return_value.cleanup.assert_called_once()
                self.assertEqual([unittest.mock.call(1200),unittest.mock.call(0)],alarm.call_args_list)

    def test_cleanup_failure_overrides_success(self):
        with patch.object(harness,"Resources") as resources, patch.object(harness,"run_suite"), \
                patch.object(harness.signal,"signal"), patch.object(harness.signal,"alarm"), patch("builtins.print"):
            resources.return_value.cleanup.side_effect = harness.HarnessError("cleanup incomplete")
            self.assertEqual(1,harness.main())


if __name__ == "__main__":
    unittest.main()
