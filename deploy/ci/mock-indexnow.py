#!/usr/bin/env python3
"""A pretend api.indexnow.org for the installer's test (deploy/ci/test-install.sh).

It takes every submission and writes it as a line of JSON into a log the test reads. (The real
service refuses a key the site does not serve; what the client does then is tested in Go.)

    mock-indexnow.py LOG PORT
"""
import json
import sys
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LOG, PORT = sys.argv[1], int(sys.argv[2])


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length") or 0)
        body = self.rfile.read(length).decode("utf-8", "replace")
        try:
            submission = json.loads(body)
        except ValueError:
            self.answer(400, "not JSON")
            return
        if self.path != "/indexnow" or not isinstance(submission.get("urlList"), list):
            self.answer(400, "not a submission")
            return
        with open(LOG, "a", encoding="utf-8") as log:
            log.write(json.dumps(submission, ensure_ascii=False) + "\n")
        self.answer(200, "OK")

    def do_GET(self):
        self.answer(405, "POST a submission")

    def answer(self, status, text):
        raw = text.encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "text/plain; charset=utf-8")
        self.send_header("Content-Length", str(len(raw)))
        self.end_headers()
        self.wfile.write(raw)

    def log_message(self, *_):
        pass


ThreadingHTTPServer(("127.0.0.1", PORT), Handler).serve_forever()
