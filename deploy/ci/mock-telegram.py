#!/usr/bin/env python3
"""A pretend Telegram Bot API for the installer's test (deploy/ci/test-install.sh).

It accepts one token, answers the few methods the bot calls, and writes every call as a line of
JSON into a log the test reads. Nothing here ever runs on a real server.

    mock-telegram.py TOKEN LOG PORT
"""
import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

TOKEN, LOG, PORT = sys.argv[1], sys.argv[2], int(sys.argv[3])


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(length).decode("utf-8", "replace")
        prefix = "/bot%s/" % TOKEN
        if not self.path.startswith(prefix):
            self.answer(401, {"ok": False, "error_code": 401, "description": "Unauthorized"})
            return
        method = self.path[len(prefix):]
        try:
            params = json.loads(body or "{}")
        except ValueError:
            params = {}
        with open(LOG, "a", encoding="utf-8") as log:
            log.write(json.dumps({"method": method, "params": params}, ensure_ascii=False) + "\n")

        result = True
        if method == "getMe":
            result = {"id": 4242, "is_bot": True, "first_name": "CI", "username": "krokosha_ci_bot"}
        elif method == "sendMessage":
            result = {"message_id": 1, "date": 0, "chat": {"id": params.get("chat_id", 0), "type": "private"}}
        elif method == "getUpdates":
            result = []
        self.answer(200, {"ok": True, "result": result})

    do_GET = do_POST

    def answer(self, code, payload):
        raw = json.dumps(payload).encode("utf-8")
        self.send_response(code)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def log_message(self, *args):
        pass


ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
