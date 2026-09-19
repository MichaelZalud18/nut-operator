"""Owned Kind hook barrier. No host operations; release is loopback-only."""
import json
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

lock = threading.Lock()
requests = {}


class Receiver(BaseHTTPRequestHandler):
    def log_message(self, *_args):
        pass

    def do_GET(self):
        with lock:
            body = json.dumps({key: {k: v for k, v in value.items() if k != "event"}
                               for key, value in requests.items()}).encode()
        self.send_response(200)
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        if self.path.startswith("/release/"):
            if self.client_address[0] != "127.0.0.1":
                self.send_error(403)
                return
            execution = self.path.removeprefix("/release/")
            with lock:
                request = requests.get(execution)
                if request is None:
                    self.send_error(404)
                    return
                request["event"].set()
            self.send_response(204)
            self.end_headers()
            return
        if self.path != "/hooks/barrier":
            self.send_error(404)
            return
        length = int(self.headers.get("Content-Length", "0"))
        if not 0 < length <= 65536:
            self.send_error(413)
            return
        data = json.loads(self.rfile.read(length))["data"]
        execution = data["executionID"]
        event = threading.Event()
        with lock:
            requests[execution] = {"event": event, "dryRun": data["dryRun"],
                                   "released": False, "timedOut": False}
        released = event.wait(120)
        with lock:
            requests[execution]["released"] = released
            requests[execution]["timedOut"] = not released
        self.send_response(202 if released else 504)
        self.end_headers()


if __name__ == "__main__":
    ThreadingHTTPServer(("0.0.0.0", 8080), Receiver).serve_forever()
