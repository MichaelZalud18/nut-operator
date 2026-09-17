#!/usr/bin/env python3
"""Run only uniquely owned disposable NetBox resources; never accept a site URL."""

import json
import os
from pathlib import Path
import re
import secrets
import signal
# Owned disposable-service orchestration.
import subprocess  # nosec B404
import sys
import tempfile
import uuid

ROOT = Path(__file__).resolve().parents[1]
LABEL = "io.nut-operator.netbox-test"
IMAGES = {
    "netbox": "docker.io/netboxcommunity/netbox:v4.7.0-5.1.1@sha256:1685e91c61bb4050089db2bb1603718820ae3ce0b266d4d069ff7c682f5d9c58",
    "postgres": "docker.io/postgres:17.11-alpine3.23@sha256:9ae4e8f8d0284836a505f0b2e825144e32e20499856e7dc5f7b99e19d10eedd6",
    "redis": "docker.io/redis:7.4.11-alpine3.21@sha256:ff02b58f971e7d7d156a1267e283fcbbeee91773b6aa36c49dac28ecfe28eadf",
}


class HarnessError(Exception):
    pass


def stop_group(process):
    # Kill descendants too, even if the direct Docker/Go client already exited.
    previous = {sig: signal.signal(sig, signal.SIG_IGN)
                for sig in (signal.SIGINT, signal.SIGTERM, signal.SIGALRM)}
    try:
        try:
            os.killpg(process.pid, signal.SIGKILL)
        except ProcessLookupError:
            pass
        try:
            process.communicate(timeout=5)
        except subprocess.TimeoutExpired:
            raise HarnessError("command group could not be reaped") from None
    finally:
        for sig, handler in previous.items():
            signal.signal(sig, handler)


def command(args, *, timeout=30, env=None, input=None, check=True):
    # Neither exception command lines nor child output are safe diagnostics.
    try:
        # Fixed local test argument vectors; no shell evaluation.
        process = subprocess.Popen(args, cwd=ROOT, env=env, stdin=subprocess.PIPE,  # nosec B603
                                   stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                                   text=True, start_new_session=True)
    except OSError:
        raise HarnessError("command could not start") from None
    try:
        stdout, stderr = process.communicate(input=input, timeout=timeout)
    except BaseException as error:
        stop_group(process)
        if isinstance(error, subprocess.TimeoutExpired):
            raise HarnessError("command deadline exceeded") from None
        raise
    result = subprocess.CompletedProcess(args, process.returncode, stdout, stderr)
    if check and result.returncode:
        raise HarnessError("command failed (output withheld)")
    return result


class Resources:
    def __init__(self, run=command):
        self.run = run
        self.owner = "nut-netbox-" + uuid.uuid4().hex
        self.resources = []
        self.network = self.owner + "-network"

    def create_network(self):
        self.resources.append(("network", self.network, ""))
        result = self.run(["docker", "network", "create", "--internal", "--label",
                           f"{LABEL}={self.owner}", self.network])
        self.resources[-1] = ("network", self.network, result.stdout.strip())

    def create_container(self, role, options=(), tail=()):
        name = self.owner + "-" + role
        # Register before create: a Docker timeout can occur after creation.
        self.resources.append(("container", name, ""))
        result = self.run(["docker", "create", "--name", name, "--label",
                           f"{LABEL}={self.owner}", "--network", self.network,
                           "--network-alias", role, *options, IMAGES[role], *tail])
        self.resources[-1] = ("container", name, result.stdout.strip())
        self.run(["docker", "start", name])
        return name

    def cleanup(self):
        failed = []
        for kind, name, identity in reversed(self.resources):
            try:
                found = self.run(["docker", kind, "inspect", identity or name], timeout=20, check=False)
                if found.returncode:
                    if "No such" in found.stderr:
                        continue
                    raise HarnessError("resource inspection failed")
                obj = json.loads(found.stdout)[0]
                labels = obj.get("Config", {}).get("Labels", {}) if kind == "container" else obj.get("Labels", {})
                if (labels or {}).get(LABEL) != self.owner:
                    raise HarnessError("resource ownership mismatch")
                object_id = obj["Id"]
                if not re.fullmatch(r"[0-9a-f]{64}", object_id) or (identity and identity != object_id):
                    raise HarnessError("resource identity mismatch")
                args = ["docker", kind, "rm"]
                if kind == "container":
                    args += ["--force", "--volumes"]
                self.run([*args, object_id], timeout=20)
            except (HarnessError, ValueError, KeyError, IndexError):
                failed.append(name)
        if failed:
            raise HarnessError("cleanup incomplete for: " + ", ".join(failed))
        self.resources.clear()


def interrupted(signum, _frame):
    raise HarnessError(f"interrupted by signal {signum}")


def run_suite(resources, directory):
    print(f"NetBox disposable owner: {resources.owner}", flush=True)
    arch = command(["docker", "info", "--format", "{{.Architecture}}"], timeout=20).stdout.strip()
    arch = {"aarch64": "arm64", "x86_64": "amd64", "arm64": "arm64", "amd64": "amd64"}.get(arch)
    if not arch:
        raise HarnessError("unsupported Docker architecture")
    build_env = dict(os.environ, CGO_ENABLED="0", GOOS="linux", GOARCH=arch)
    for output, args in [("netbox-inventory-sync", ["build", "./cmd/netbox-inventory-sync"]),
                         ("netbox.test", ["test", "-c", "./test/netbox"])]:
        print("Building " + output, flush=True)
        command(["go", *args[:-1], "-o", str(directory / output), args[-1]],
                timeout=300, env=build_env)
    for image in IMAGES.values():
        print("Pulling " + image, flush=True)
        command(["docker", "pull", image], timeout=240)

    password = secrets.token_hex(32)
    redis_password = secrets.token_hex(32)
    environment = {
        "POSTGRES_DB": "netbox", "POSTGRES_USER": "netbox", "POSTGRES_PASSWORD": password,
        "DB_NAME": "netbox", "DB_USER": "netbox", "DB_PASSWORD": password, "DB_HOST": "postgres",
        "REDIS_HOST": "redis", "REDIS_PASSWORD": redis_password,
        "REDIS_CACHE_HOST": "redis", "REDIS_CACHE_PASSWORD": redis_password,
        "REDIS_DATABASE": "0", "REDIS_CACHE_DATABASE": "1",
        "SECRET_KEY": secrets.token_hex(40), "API_TOKEN_PEPPER_1": secrets.token_hex(40),
        "SKIP_SUPERUSER": "true", "LOGIN_REQUIRED": "true", "ALLOWED_HOSTS": "localhost 127.0.0.1",
        "MAX_PAGE_SIZE": "1", "CENSUS_REPORTING_ENABLED": "false", "ISOLATED_DEPLOYMENT": "true",
        "COPILOT_ENABLED": "false", "GRANIAN_WORKERS": "1",
    }
    env_file = directory / "service.env"
    env_file.touch(mode=0o600)
    env_file.write_text("".join(f"{key}={value}\n" for key, value in environment.items()))
    resources.create_network()
    resources.create_container("postgres", ["--env-file", str(env_file),
                                            "--tmpfs", "/var/lib/postgresql/data:rw"])
    resources.create_container("redis", ["--env-file", str(env_file), "--tmpfs", "/data:rw"],
                               ["sh", "-c", 'exec redis-server --save "" --appendonly no --requirepass "$REDIS_PASSWORD"'])
    netbox = resources.create_container("netbox", ["--env-file", str(env_file), "--user", "netbox:root"])
    print("Waiting for isolated NetBox HTTP readiness", flush=True)
    command(["docker", "exec", "-i", netbox, "/opt/netbox/venv/bin/python", "-"],
            input=(ROOT / "test/netbox/wait.py").read_text(), timeout=330)
    for name in ("netbox-inventory-sync", "netbox.test"):
        command(["docker", "cp", str(directory / name), f"{netbox}:/tmp/{name}"])
    command(["docker", "exec", "-i", netbox, "/opt/netbox/venv/bin/python", "manage.py", "shell",
             "--no-startup", "--no-imports", "--interface", "python"],
            input=(ROOT / "test/netbox/bootstrap.py").read_text(), timeout=60)
    print("Running real REST/CLI contract assertions", flush=True)
    # The executable path is inside this run's new, owned disposable container.
    result = command(["docker", "exec", "--env", "NETBOX_DISPOSABLE_TEST=1", netbox,
                      "/tmp/netbox.test", "-test.run=^TestRealNetBox$", "-test.v", "-test.timeout=3m"],  # nosec B108
                     timeout=200, check=False)
    # The test prints only fixed assertion messages, never provider data or credentials.
    print(result.stdout, end="", flush=True)
    if result.returncode:
        raise HarnessError("real NetBox contract assertions failed (stderr withheld)")


def main():
    resources = Resources()
    for sig in (signal.SIGINT, signal.SIGTERM, signal.SIGALRM):
        signal.signal(sig, interrupted)
    signal.alarm(1200)
    result = 0
    try:
        with tempfile.TemporaryDirectory(prefix="nut-netbox-") as directory:
            run_suite(resources, Path(directory))
    except HarnessError as error:
        print(f"NetBox test: {error}", file=sys.stderr)
        result = 1
    finally:
        signal.alarm(0)
        # A second termination must not interrupt the bounded cleanup pass.
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        try:
            resources.cleanup()
            print(f"NetBox cleanup complete: {resources.owner}", flush=True)
        except HarnessError as error:
            print(str(error), file=sys.stderr)
            result = 1
    return result


if __name__ == "__main__":
    sys.exit(main())
