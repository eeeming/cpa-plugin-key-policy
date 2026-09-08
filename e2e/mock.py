#!/usr/bin/env python3
"""Mock OpenAI-compatible chat completions only. Prices come from Home."""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import threading

lock = threading.Lock()
chat_calls = 0


class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        print(f"[mock-llm] {self.command} {self.path} {fmt % args}", flush=True)

    def _send(self, status, payload):
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        path = self.path.split("?", 1)[0]
        if path in ("/healthz", "/"):
            self._send(200, {"ok": True, "chat_calls": chat_calls})
            return
        self._send(404, {"error": "not found", "path": path})

    def do_POST(self):
        global chat_calls
        path = self.path.split("?", 1)[0]
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b""
        if path in ("/v1/chat/completions", "/chat/completions"):
            with lock:
                chat_calls += 1
                n = chat_calls
            print(f"[mock-llm] completion #{n} body={raw[:180]!r}", flush=True)
            self._send(
                200,
                {
                    "id": f"chatcmpl-e2e-{n}",
                    "object": "chat.completion",
                    "created": 1,
                    "model": "gpt-4.1-mini",
                    "choices": [
                        {
                            "index": 0,
                            "message": {"role": "assistant", "content": f"ok-{n}"},
                            "finish_reason": "stop",
                        }
                    ],
                    "usage": {
                        "prompt_tokens": 1000,
                        "completion_tokens": 0,
                        "total_tokens": 1000,
                    },
                },
            )
            return
        self._send(404, {"error": "not found", "path": path})


if __name__ == "__main__":
    server = ThreadingHTTPServer(("0.0.0.0", 8080), Handler)
    print("[mock-llm] listening on :8080", flush=True)
    server.serve_forever()
