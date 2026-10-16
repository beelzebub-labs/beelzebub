"""Synthetic Platform fixture; exposed only on a disposable Docker network."""

import base64
import json
from pathlib import Path
import re
import socket
import sys
import threading
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.request import Request, urlopen


SERVICE = '''apiVersion: "v1"
protocol: "http"
address: ":8081"
description: "Synthetic durable event test"
commands:
  - regex: ".*"
    handler: "synthetic"
    statusCode: 200
'''
STATE = {"status": 500, "attempts": [], "accepted": {}, "lost_ack": False, "dropped": []}
LOCK = threading.Lock()


class Platform(BaseHTTPRequestHandler):
    def log_message(self, *_):
        pass

    def respond(self, status, value):
        body = json.dumps(value).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/cloud/honeypots":
            self.respond(200, [{"id": "synthetic", "config": SERVICE}])
        elif self.path == "/state":
            with LOCK:
                self.respond(200, STATE)
        else:
            self.respond(404, {})

    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", "0")))
        if self.path == "/control":
            with LOCK:
                STATE.update(json.loads(body))

            self.respond(200, {})
            return

        if self.path != "/cloud/events":
            self.respond(404, {})
            return

        event = json.loads(body)
        key = event["EventKey"]
        encoded = base64.b64encode(body).decode()

        with LOCK:
            status = STATE["status"]
            STATE["attempts"].append({"key": key, "payload": encoded, "status": status})
            drop = status == 200 and STATE["lost_ack"] and key not in STATE["dropped"]

            if status == 200:
                previous = STATE["accepted"].setdefault(key, encoded)

                if previous != encoded:
                    self.respond(409, {})
                    return

            if drop:
                STATE["dropped"].append(key)

        if drop:
            self.connection.shutdown(socket.SHUT_RDWR)
            self.connection.close()
            return

        self.respond(status, event if status == 200 else {})


def main():
    mode = sys.argv[1]
    if mode == "serve":
        ThreadingHTTPServer(("0.0.0.0", 8080), Platform).serve_forever()
    elif mode == "snapshot":
        records = []

        for path in sorted(Path(sys.argv[2] if len(sys.argv) > 2 else "/spool").glob("*.json")):
            body = path.read_text()
            envelope = json.loads(body)
            start = re.search(r'"payload"\s*:\s*', body).end()
            _, end = json.JSONDecoder().raw_decode(body, start)

            records.append({"key": envelope["eventKey"], "payload": envelope["payload"],
                            "wirePayload": base64.b64encode(body[start:end].encode()).decode(),
                            "uid": path.stat().st_uid, "mode": path.stat().st_mode & 0o777})

        print(json.dumps(records))
    elif mode in ("trigger", "trigger-large"):
        data = b"x" * 70000 if mode == "trigger-large" else None
        request = Request("http://sensor:8081/capture/" + sys.argv[2], data=data)
        with urlopen(request, timeout=10) as response:
            assert response.status == 200

        print("true")
    else:
        data = sys.argv[3].encode() if len(sys.argv) > 3 else None
        request = Request("http://127.0.0.1:8080" + sys.argv[2], data=data,
                          headers={"Content-Type": "application/json"})

        with urlopen(request, timeout=10) as response:
            print(response.read().decode())


if __name__ == "__main__":
    main()
