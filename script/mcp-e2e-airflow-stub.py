#!/usr/bin/env python3
"""Minimal Airflow REST stand-in for script/mcp-e2e.sh bring-up mode.

The backend's import path refuses to leave a jobless CONVERTING row behind:
it triggers an Airflow DAG run with a deterministic dag_run_id and verifies
the response echoes that exact id. This stub implements just enough of the
Airflow API for that dispatch handshake to succeed — the conversion itself
never runs, so imported models deterministically stay in state "converting"
(which is exactly what the bounded-wait conversion-status leg asserts).

Endpoints:
  POST /auth/token            -> static bearer, 1h expiry
  POST /api/v2/dags/*/dagRuns -> echoes the requested dag_run_id as queued

No credentials are involved: the token it mints is the literal string
"mcp-e2e-stub-token" and the backend only talks to it over localhost.
"""

import json
import sys
from http.server import BaseHTTPRequestHandler, HTTPServer


class Handler(BaseHTTPRequestHandler):
    def _send(self, code, obj):
        body = json.dumps(obj).encode()
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        length = int(self.headers.get("Content-Length") or 0)
        try:
            payload = json.loads(self.rfile.read(length) or b"{}")
        except json.JSONDecodeError:
            payload = {}
        if self.path == "/auth/token":
            self._send(200, {"access_token": "mcp-e2e-stub-token", "expires_in": 3600})
        elif "/dagRuns" in self.path:
            self._send(200, {"dag_run_id": payload.get("dag_run_id"), "state": "queued"})
        else:
            self._send(404, {"detail": "not found"})

    def do_GET(self):
        self._send(404, {"detail": "not found"})

    def log_message(self, fmt, *args):
        sys.stderr.write("airflow-stub: " + fmt % args + "\n")


def main():
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 18100
    HTTPServer(("0.0.0.0", port), Handler).serve_forever()


if __name__ == "__main__":
    main()
