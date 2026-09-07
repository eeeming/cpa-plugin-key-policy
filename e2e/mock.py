#!/usr/bin/env python3
"""Mock Plus price table + OpenAI-compatible chat completions for CPA E2E."""

from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import threading

PRICES = {
    "items": [
        {
            "provider": "",
            "model": "gpt-4.1-mini",
            "service_tier": "*",
            "min_input_tokens": 0,
            "input_price_per_million": 1000.0,
            "output_price_per_million": 0.0,
            "cache_read_price_per_million": 0.0,
            "request_price": 0.0,
            "enabled": True,
        }
    ]
}

lock = threading.Lock()
chat_calls = 0


class Handler(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        print(f"[mock] {self.command} {self.path} {fmt % args}", flush=True)

    def _send(self, status, payload, content_type="application/json"):
        body = payload if isinstance(payload, (bytes, bytearray)) else json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        path = self.path.split("?", 1)[0]
        if path in ("/healthz", "/"):
            self._send(200, {"ok": True, "chat_calls": chat_calls})
            return
        if path == "/v0/management/billing/model-prices":
            self._send(200, PRICES)
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
            print(f"[mock] chat completion #{n} auth={self.headers.get('Authorization')!r} body={raw[:200]!r}", flush=True)
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


def main():
    server = ThreadingHTTPServer(("0.0.0.0", 8080), Handler)
    print("[mock] listening on :8080", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    main()
