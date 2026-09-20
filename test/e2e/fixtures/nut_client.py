"""NUT-only acceptance client: verified STARTTLS, bounded reads, no host actions."""
import hashlib
import json
from pathlib import Path
import shlex
import socket
import ssl
import sys


def main():
    host, ups, variable, mode = sys.argv[1:5]
    sock = socket.create_connection((host, 3493), timeout=4)

    def line():
        data = bytearray()
        while not data.endswith(b"\n"):
            chunk = sock.recv(1)
            if not chunk:
                raise RuntimeError("closed connection")
            data.extend(chunk)
            if len(data) > 65536:
                raise RuntimeError("oversized protocol response")
        return data.decode().strip()

    def command(value):
        sock.sendall((value + "\n").encode())
        return line()

    if command("STARTTLS") != "OK STARTTLS":
        raise RuntimeError("STARTTLS refused")
    ctx = ssl.create_default_context(cafile="/fixture/ca.crt" if mode != "wrong-ca" else None)
    sock = ctx.wrap_socket(sock, server_hostname=host if mode != "wrong-name" else "wrong.invalid")
    fingerprint = hashlib.sha256(sock.getpeercert(binary_form=True)).hexdigest()
    password = Path("/credentials/" + ("old-password" if mode == "old-password" else "password")).read_text()
    if mode == "bad-password":
        password = "deliberately-wrong"
    for value in ("USERNAME monitor", "PASSWORD " + json.dumps(password), "LOGIN " + ups):
        response = command(value)
        if response != "OK":
            raise RuntimeError("authentication refused: " + response)
    response = command("GET VAR " + ups + " " + variable)
    fields = shlex.split(response)
    if len(fields) != 4 or fields[:3] != ["VAR", ups, variable]:
        raise RuntimeError("telemetry unavailable: " + response)
    print(json.dumps({"value": fields[3], "certificate": fingerprint}))
    sock.close()


if __name__ == "__main__":
    try:
        main()
    except Exception as error:
        print(type(error).__name__ + ": " + str(error), file=sys.stderr)
        sys.exit(1)
