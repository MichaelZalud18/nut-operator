"""Both guest adapters' emergency ownership contracts, without VMs or KVM."""

from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

import vm_cleanup


class SharedCleanupTest(unittest.TestCase):
    def test_exact_boundary_and_monitor(self):
        with tempfile.TemporaryDirectory(prefix="hadron-smoke-") as name:
            root = Path(name)
            for arg in (f"file={root}/disk,if=none", f"unix:{root}/qemu-monitor.sock,server,nowait"):
                self.assertTrue(vm_cleanup.owns_command(["qemu-system-x86_64", arg], root))
            for argv in ([], ["bash", str(root) + "/vm"], ["qemu-system-x86_64", str(root) + "-other/vm"]):
                self.assertFalse(vm_cleanup.owns_command(argv, root))

    def test_both_guest_roots_preserve_foreign_process_and_diagnostics(self):
        for guest in ("hadron", "talos"):
            with self.subTest(guest=guest), tempfile.TemporaryDirectory(prefix=guest + "-smoke-") as root:
                marker = Path(root) / "console.log"
                marker.write_text("retain diagnostics")
                processes = [subprocess.Popen(
                    ["qemu-system-x86_64", "-c", "import time; time.sleep(30)", arg], executable=sys.executable
                ) for arg in (root + "/vm", root + "-other/vm")]
                try:
                    vm_cleanup.cleanup(root)
                    self.assertEqual(processes[0].wait(timeout=2), -9)
                    self.assertIsNone(processes[1].poll())
                    self.assertEqual(marker.read_text(), "retain diagnostics")
                    vm_cleanup.cleanup(root)
                finally:
                    for process in processes:
                        if process.poll() is None:
                            process.kill()
                        process.wait(timeout=2)

    def test_unowned_root_is_rejected(self):
        with tempfile.TemporaryDirectory(prefix="unowned-") as root:
            with self.assertRaises(ValueError):
                vm_cleanup.cleanup(root)


if __name__ == "__main__":
    unittest.main()
