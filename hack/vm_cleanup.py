#!/usr/bin/env python3
"""Shared emergency QEMU cleanup for Hadron and Talos private smoke roots.

This remains independent of Go so workflow cleanup survives a failed test process.
It retains diagnostics; callers remove state only after cleanup succeeds.
"""

import os
from pathlib import Path
import select
import signal
import sys


def owns_command(argv, root):
    """Match the QEMU command and exact root boundary, including PEG's monitor."""
    if not argv or Path(argv[0]).name != "qemu-system-x86_64":
        return False
    for arg in argv[1:]:
        for field in arg.split(","):
            value = field.split("=", 1)[-1]
            if value.startswith("unix:"):
                value = value[5:]
            if value.startswith(str(root) + "/"):
                return True
    return False


def cleanup(root):
    root = Path(root).resolve(strict=True)
    if not root.is_dir() or not root.name.startswith(("hadron-smoke-", "talos-smoke-")):
        raise ValueError("expected a private Hadron or Talos smoke state directory")
    for proc in Path("/proc").iterdir():
        if not proc.name.isdecimal():
            continue
        fd = None
        try:
            fd = os.pidfd_open(int(proc.name))
            argv = (proc / "cmdline").read_bytes().decode(errors="surrogateescape").rstrip("\0").split("\0")
            if owns_command(argv, root):
                signal.pidfd_send_signal(fd, signal.SIGKILL)
                poll = select.poll()
                poll.register(fd, select.POLLIN)
                if not poll.poll(5000):
                    raise TimeoutError("owned QEMU did not exit; preserving state")
        except (ProcessLookupError, FileNotFoundError):
            pass
        finally:
            if fd is not None:
                os.close(fd)


if __name__ == "__main__":
    cleanup(sys.argv[1])
