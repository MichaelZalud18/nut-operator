#!/usr/bin/env python3
"""Stop only QEMU processes referencing this smoke run's private state root.

Linux pidfds prevent PID reuse between inspection and termination. Keep state for
diagnostics; the workflow removes it only after successful process cleanup.
"""

import os
from pathlib import Path
import select
import signal
import sys


def owns_command(argv, root):
    return bool(argv) and Path(argv[0]).name == "qemu-system-x86_64" and any(
        part.startswith(str(root) + "/")
        for arg in argv[1:]
        for field in arg.split(",")
        for part in [field.split("=", 1)[-1]]
    )


def cleanup(root):
    root = Path(root).resolve(strict=True)
    if not root.is_dir() or not root.name.startswith("hadron-smoke-"):
        raise ValueError("expected a private hadron-smoke-* state directory")
    for proc in Path("/proc").iterdir():
        if not proc.name.isdecimal():
            continue
        fd = None
        try:
            fd = os.pidfd_open(int(proc.name))
            argv = (proc / "cmdline").read_bytes().decode().rstrip("\0").split("\0")
            if owns_command(argv, root):
                signal.pidfd_send_signal(fd, signal.SIGKILL)
                poll = select.poll()
                poll.register(fd, select.POLLIN)
                if not poll.poll(5000):
                    raise TimeoutError(f"QEMU {proc.name} did not exit; preserving state")
        except (ProcessLookupError, FileNotFoundError):
            pass
        finally:
            if fd is not None:
                os.close(fd)


if __name__ == "__main__":
    cleanup(sys.argv[1])
