#!/usr/bin/env python3
"""Mock ChatGPT Codex backend for the Codex-binding E2E.

CPA's Codex OAuth executor always calls https://chatgpt.com/backend-api/codex
(OAuth auth files cannot override base_url). The compose stack gives this
container the network alias chatgpt.com and makes CPA trust a throwaway CA, so
the real executor/TLS path is exercised. Every upstream call is recorded with
the account it carried (Bearer access token -> account letter), letting the
test prove which Codex account served each downstream request.

:443  HTTPS  POST /backend-api/codex/responses (SSE, like the real backend)
:8080 HTTP   GET /healthz, GET /calls, POST /reset   (test control plane)
"""

from __future__ import annotations

import json
import ssl
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

TOKENS = {"tok-codex-a": "A", "tok-codex-b": "B", "tok-codex-c": "C"}
lock = threading.Lock()
calls: list[dict] = []
seq = 0


def sse(event: str, data: dict) -> bytes:
    return f"event: {event}\ndata: {json.dumps(data)}\n\n".encode()


class Upstream(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        print(f"[mock-codex] {self.command} {self.path} {fmt % args}", flush=True)

    def _json(self, status: int, payload) -> None:
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        self._json(404, {"error": "not found", "path": self.path})

    def do_POST(self):
        global seq
        path = self.path.split("?", 1)[0]
        length = int(self.headers.get("Content-Length") or 0)
        raw = self.rfile.read(length) if length else b""
        if path != "/backend-api/codex/responses":
            # Token refresh or anything else must never be needed in this test.
            print(f"[mock-codex] UNEXPECTED {path} body={raw[:200]!r}", flush=True)
            self._json(404, {"error": "unexpected", "path": path})
            return
        auth = self.headers.get("Authorization", "")
        token = auth[7:] if auth.startswith("Bearer ") else ""
        account = TOKENS.get(token, "?")
        try:
            body = json.loads(raw.decode() or "{}")
        except Exception:
            body = {}
        with lock:
            seq += 1
            n = seq
            calls.append(
                {
                    "n": n,
                    "account": account,
                    "chatgpt_account_id": self.headers.get("Chatgpt-Account-Id", ""),
                    "model": body.get("model"),
                    "stream": body.get("stream"),
                    "header_names": sorted(k.lower() for k in self.headers.keys()),
                    "session_id": self.headers.get("Session_id") or self.headers.get("Session-Id") or "",
                    "ts": time.time(),
                }
            )
        print(f"[mock-codex] call #{n} account={account} model={body.get('model')}", flush=True)
        model = body.get("model") or "gpt-5"
        text = f"acct-{account}-{n}"
        rid = f"resp_e2e_{n}"
        msg = {
            "id": f"msg_{n}",
            "type": "message",
            "status": "completed",
            "role": "assistant",
            "content": [{"type": "output_text", "text": text, "annotations": []}],
        }
        usage = {
            "input_tokens": 1000,
            "input_tokens_details": {"cached_tokens": 0},
            "output_tokens": 0,
            "output_tokens_details": {"reasoning_tokens": 0},
            "total_tokens": 1000,
        }
        base = {"id": rid, "object": "response", "created_at": int(time.time()), "model": model}
        frames = [
            sse("response.created", {"type": "response.created", "sequence_number": 0, "response": {**base, "status": "in_progress", "output": []}}),
            sse("response.output_item.added", {"type": "response.output_item.added", "sequence_number": 1, "output_index": 0, "item": {**msg, "status": "in_progress", "content": []}}),
            sse("response.content_part.added", {"type": "response.content_part.added", "sequence_number": 2, "item_id": msg["id"], "output_index": 0, "content_index": 0, "part": {"type": "output_text", "text": "", "annotations": []}}),
            sse("response.output_text.delta", {"type": "response.output_text.delta", "sequence_number": 3, "item_id": msg["id"], "output_index": 0, "content_index": 0, "delta": text}),
            sse("response.output_text.done", {"type": "response.output_text.done", "sequence_number": 4, "item_id": msg["id"], "output_index": 0, "content_index": 0, "text": text}),
            sse("response.content_part.done", {"type": "response.content_part.done", "sequence_number": 5, "item_id": msg["id"], "output_index": 0, "content_index": 0, "part": {"type": "output_text", "text": text, "annotations": []}}),
            sse("response.output_item.done", {"type": "response.output_item.done", "sequence_number": 6, "output_index": 0, "item": msg}),
            sse("response.completed", {"type": "response.completed", "sequence_number": 7, "response": {**base, "status": "completed", "output": [msg], "usage": usage}}),
        ]
        payload = b"".join(frames)
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Content-Length", str(len(payload)))
        self.end_headers()
        self.wfile.write(payload)


class Control(BaseHTTPRequestHandler):
    def log_message(self, fmt, *args):
        pass

    def _json(self, status: int, payload) -> None:
        body = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        path = self.path.split("?", 1)[0]
        if path == "/healthz":
            self._json(200, {"ok": True})
        elif path == "/calls":
            with lock:
                self._json(200, {"calls": list(calls)})
        else:
            self._json(404, {"error": "not found"})

    def do_POST(self):
        global calls
        if self.path.split("?", 1)[0] == "/reset":
            with lock:
                calls = []
            self._json(200, {"ok": True})
        else:
            self._json(404, {"error": "not found"})


def serve_tls() -> None:
    server = ThreadingHTTPServer(("0.0.0.0", 443), Upstream)
    ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    ctx.load_cert_chain("/certs/server.pem", "/certs/server.key")
    ctx.set_alpn_protocols(["http/1.1"])
    server.socket = ctx.wrap_socket(server.socket, server_side=True)
    print("[mock-codex] TLS listening on :443", flush=True)
    server.serve_forever()


if __name__ == "__main__":
    threading.Thread(target=serve_tls, daemon=True).start()
    control = ThreadingHTTPServer(("0.0.0.0", 8080), Control)
    print("[mock-codex] control listening on :8080", flush=True)
    control.serve_forever()
