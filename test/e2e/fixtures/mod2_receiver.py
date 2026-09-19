"""Harmless Kind-only receiver: records requests, never invokes host operations."""
import json
import os
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

lock = threading.Lock()
state = {"requests": [], "effects": 0, "stopped": False}


class Receiver(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass  # Never log headers or credentials.

    def do_GET(self):
        with lock:
            body = json.dumps(state).encode()
        self.send_response(200)
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        if self.headers.get("Authorization") != "Bearer " + os.environ["HOOK_TOKEN"]:
            self.send_error(401)
            return
        length = int(self.headers.get("Content-Length", "0"))
        if not 0 < length <= 65536:
            self.send_error(413)
            return
        event = json.loads(self.rfile.read(length))
        data = event["data"]
        if data["extensions"].get("host") != "external-a":
            self.send_error(400)
            return
        with lock:
            state["requests"].append({"execution": data["executionID"],
                                      "group": data["group"], "dryRun": data["dryRun"],
                                      "path": self.path})
            if self.path == "/hooks/stop" and not data["dryRun"] and not state["stopped"]:
                state["stopped"] = True
                state["effects"] += 1
        if self.path == "/hooks/timeout":
            time.sleep(3)
        self.send_response(503 if self.path == "/hooks/fail" else 202)
        self.end_headers()


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", 8080), Receiver).serve_forever()
