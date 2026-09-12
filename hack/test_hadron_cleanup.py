"""KVM-free ownership and process-cleanup regression tests."""

import importlib.util
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

spec = importlib.util.spec_from_file_location("cleanup", Path(__file__).with_name("hadron-cleanup.py"))
module = importlib.util.module_from_spec(spec)
spec.loader.exec_module(module)


class CleanupTest(unittest.TestCase):
    def test_exact_owner(self):
        root = Path("/tmp/hadron-smoke-test")  # nosec B108 -- a Path value for owns_command's string matching, never read or written
        self.assertTrue(module.owns_command(["qemu-system-x86_64", "-drive", f"file={root}/vm/disk,if=none"], root))
        for argv in [[], ["bash", str(root) + "/vm"], ["qemu-system-x86_64", str(root) + "-other/vm"]]:
            self.assertFalse(module.owns_command(argv, root))

    def test_stops_only_owned_process(self):
        with tempfile.TemporaryDirectory(prefix="hadron-smoke-") as root:
            # Substitute harmless Python sleepers with QEMU-shaped argv; never start a VM.
            processes = [subprocess.Popen(["qemu-system-x86_64", "-c", "import time; time.sleep(60)", arg], executable=sys.executable)
                         for arg in [root + "/vm", root + "-other/vm"]]
            try:
                module.cleanup(root)
                self.assertEqual(processes[0].wait(timeout=2), -9)
                self.assertIsNone(processes[1].poll())
                module.cleanup(root)  # Repeated cleanup is safe.
            finally:
                for process in processes:
                    if process.poll() is None:
                        process.kill()
                    process.wait(timeout=2)


if __name__ == "__main__":
    unittest.main()
