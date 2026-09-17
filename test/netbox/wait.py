"""Bounded HTTP readiness inside the service; no host port or secret output."""
import time
import urllib.error
import urllib.request

deadline = time.monotonic() + 300
while time.monotonic() < deadline:
    try:
        # Fixed loopback HTTP URL inside an owned isolated container; no caller URL.
        with urllib.request.urlopen("http://127.0.0.1:8080/login/", timeout=3) as response:  # nosec B310
            if response.status == 200:
                break
    except (urllib.error.URLError, TimeoutError, OSError):
        pass
    time.sleep(2)
else:
    raise SystemExit("NetBox readiness deadline exceeded")
