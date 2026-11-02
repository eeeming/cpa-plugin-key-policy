#!/usr/bin/env python3
"""Local native-provider backends for the real CPA executor E2E.

TLS :443 serves Codex responses (chatgpt.com) and Claude OAuth messages
(api.anthropic.com). HTTP :8080 serves OpenAI-compatible chat completions,
Gemini generateContent/streamGenerateContent, and /healthz, /calls, /reset,
/hold, /gate, /release test controls. Only fixture credentials and explicit
provider endpoints are accepted; nothing is proxied.

Calls retain parsed and raw request bodies and all incoming header pairs. A
hold claims exactly one matching recorded call, then waits without holding the
recording lock, so unrelated traffic can complete while that call is pending.
"""

from __future__ import annotations

import json
import ssl
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import parse_qs, urlsplit

TOKENS = {"tok-codex-a": "A", "tok-codex-b": "B", "tok-codex-c": "C"}
OPENAI_TOKENS = {"sk-upstream-openai": "openai-native"}
CLAUDE_TOKENS = {"tok-claude-a": "claude-native"}
GEMINI_TOKENS = {"tok-gemini-a": "gemini-native"}
HOLD_TIMEOUT = 60
lock = threading.Lock()
calls: list[dict] = []
seq = 0


class Gate:
    def __init__(self, account: str, match: str, status: int):
        self.account = account
        self.match = match
        self.status = status
        self.n: int | None = None
        self.release = threading.Event()


gate: Gate | None = None


def sse(event: str, data: dict) -> bytes:
    return f"event: {event}\ndata: {json.dumps(data)}\n\n".encode()


class Upstream(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def log_message(self, fmt, *args):
        print(f"[mock-codex] {self.command} {self.path} {fmt % args}", flush=True)

    def _send(self, status: int, content_type: str, body: bytes) -> None:
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def _json(self, status: int, payload) -> None:
        self._send(status, "application/json", json.dumps(payload).encode())

    def _body(self) -> tuple[bytes, dict] | None:
        try:
            length = int(self.headers.get("Content-Length") or 0)
            if length < 0 or self.headers.get("Transfer-Encoding"):
                raise ValueError("a nonnegative Content-Length is required")
            raw = self.rfile.read(length)
            body = json.loads(raw.decode() or "{}")
            if not isinstance(body, dict):
                raise ValueError("request body must be a JSON object")
        except (ValueError, UnicodeDecodeError) as exc:
            self._json(400, {"error": {"message": str(exc), "type": "invalid_request_error"}})
            return None
        return raw, body

    def _error(self, provider: str, status: int, message: str) -> None:
        if provider == "claude":
            error_type = "authentication_error" if status == 401 else "api_error"
            self._json(status, {"type": "error", "error": {"type": error_type, "message": message}})
        elif provider == "gemini":
            error_type = {401: "UNAUTHENTICATED", 504: "DEADLINE_EXCEEDED"}.get(status, "INTERNAL")
            self._json(status, {"error": {"code": status, "status": error_type, "message": message}})
        else:
            error_type = "authentication_error" if status == 401 else "server_error"
            self._json(status, {"error": {"type": error_type, "code": "mock_upstream_error", "message": message}})

    def do_GET(self):
        self._json(404, {"error": "not found", "path": self.path})

    def do_POST(self):
        path = self.path.split("?", 1)[0]
        if path == "/backend-api/codex/responses":
            self._provider("codex", TOKENS)
        elif path == "/v1/messages":
            self._provider("claude", CLAUDE_TOKENS)
        else:
            # Refresh and other endpoints must never be needed in this fixture.
            self.close_connection = True
            self._json(404, {"error": "unexpected", "path": path})

    def _provider(self, provider: str, tokens: dict[str, str], *, model: str | None = None, stream: bool | None = None) -> None:
        global seq
        parsed = self._body()
        if parsed is None:
            return
        raw, body = parsed
        if provider == "gemini":
            query = parse_qs(urlsplit(self.path).query, keep_blank_values=True)
            header = self.headers.get("X-Goog-Api-Key")
            keys = ([header] if header is not None else []) + query.get("key", [])
            token = keys[0] if keys and len(set(keys)) == 1 else ""
        else:
            auth = self.headers.get("Authorization", "")
            token = auth[7:] if auth.startswith("Bearer ") else ""
        account = tokens.get(token)
        if account is None:
            self._error(provider, 401, "Unknown fixture credential")
            return
        held = None
        with lock:
            seq += 1
            n = seq
            calls.append(
                {
                    "n": n,
                    "provider": provider,
                    "account": account,
                    "chatgpt_account_id": self.headers.get("Chatgpt-Account-Id", ""),
                    "model": model if model is not None else body.get("model"),
                    "stream": stream if stream is not None else body.get("stream"),
                    "header_names": sorted(k.lower() for k in self.headers.keys()),
                    "headers": list(self.headers.raw_items()),
                    "body": body,
                    "raw_body": raw.decode(),
                    "session_id": self.headers.get("Session_id") or self.headers.get("Session-Id") or "",
                    "ts": time.time(),
                }
            )
            if gate is not None and gate.n is None and not gate.release.is_set():
                if gate.account == account and gate.match in json.dumps(body):
                    gate.n = n
                    held = gate
        print(f"[mock-codex] call #{n} provider={provider} account={account} model={model or body.get('model')}", flush=True)
        if held is not None:
            if not held.release.wait(HOLD_TIMEOUT):
                held.release.set()
                self._error(provider, 504, "Fixture hold timed out before release")
                return
            if held.status == 500:
                self._error(provider, 500, "Fixture upstream failure after release")
                return
        text = f"acct-{account}-{n}"
        if provider == "codex":
            self._codex(body, n, text)
        elif provider == "openai":
            self._openai(body, n, text)
        elif provider == "claude":
            self._claude(body, n, text)
        elif provider == "gemini":
            self._gemini(model, bool(stream), n, text)

    def _codex(self, body: dict, n: int, text: str) -> None:
        model = body.get("model") or "gpt-5"
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
        self._send(200, "text/event-stream", b"".join(frames))

    def _openai(self, body: dict, n: int, text: str) -> None:
        base = {
            "id": f"chatcmpl_e2e_{n}",
            "object": "chat.completion",
            "created": int(time.time()),
            "model": body.get("model"),
        }
        usage = {"prompt_tokens": 1000, "completion_tokens": 0, "total_tokens": 1000}
        if not body.get("stream"):
            self._json(200, {
                **base,
                "choices": [{"index": 0, "message": {"role": "assistant", "content": text}, "finish_reason": "stop"}],
                "usage": usage,
            })
            return
        base["object"] = "chat.completion.chunk"
        chunks = [
            {**base, "choices": [{"index": 0, "delta": {"role": "assistant", "content": ""}, "finish_reason": None}]},
            {**base, "choices": [{"index": 0, "delta": {"content": text}, "finish_reason": None}]},
            {**base, "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}]},
            {**base, "choices": [], "usage": usage},
        ]
        payload = b"".join(f"data: {json.dumps(chunk)}\n\n".encode() for chunk in chunks)
        self._send(200, "text/event-stream", payload + b"data: [DONE]\n\n")

    def _claude(self, body: dict, n: int, text: str) -> None:
        usage = {
            "input_tokens": 1000,
            "output_tokens": 0,
            "cache_creation_input_tokens": 0,
            "cache_read_input_tokens": 0,
        }
        message = {
            "id": f"msg_e2e_{n}",
            "type": "message",
            "role": "assistant",
            "model": body.get("model"),
            "content": [{"type": "text", "text": text}],
            "stop_reason": "end_turn",
            "stop_sequence": None,
            "usage": usage,
        }
        if not body.get("stream"):
            self._json(200, message)
            return
        frames = [
            sse("message_start", {"type": "message_start", "message": {**message, "content": [], "stop_reason": None}}),
            sse("content_block_start", {"type": "content_block_start", "index": 0, "content_block": {"type": "text", "text": ""}}),
            sse("content_block_delta", {"type": "content_block_delta", "index": 0, "delta": {"type": "text_delta", "text": text}}),
            sse("content_block_stop", {"type": "content_block_stop", "index": 0}),
            sse("message_delta", {"type": "message_delta", "delta": {"stop_reason": "end_turn", "stop_sequence": None}, "usage": {"output_tokens": 0}}),
            sse("message_stop", {"type": "message_stop"}),
        ]
        self._send(200, "text/event-stream", b"".join(frames))

    def _gemini(self, model: str, stream: bool, n: int, text: str) -> None:
        response = {
            "candidates": [{
                "content": {"role": "model", "parts": [{"text": text}]},
                "finishReason": "STOP",
                "index": 0,
            }],
            "usageMetadata": {"promptTokenCount": 1000, "candidatesTokenCount": 0, "totalTokenCount": 1000},
            "modelVersion": model,
            "responseId": f"gemini_e2e_{n}",
        }
        if stream:
            self._send(200, "text/event-stream", f"data: {json.dumps(response)}\n\n".encode())
        else:
            self._json(200, response)


class Control(Upstream):
    def log_message(self, fmt, *args):
        pass

    def do_GET(self):
        path = self.path.split("?", 1)[0]
        if path == "/healthz":
            self._json(200, {"ok": True})
        elif path == "/calls":
            with lock:
                snapshot = list(calls)
            self._json(200, {"calls": snapshot})
        elif path == "/gate":
            with lock:
                state = {
                    "held": gate is not None and gate.n is not None and not gate.release.is_set(),
                    "released": gate is not None and gate.release.is_set(),
                    "n": gate.n if gate is not None else None,
                }
            self._json(200, state)
        else:
            self._json(404, {"error": "not found"})

    def do_POST(self):
        global gate
        path = self.path.split("?", 1)[0]
        if path == "/v1/chat/completions":
            self._provider("openai", OPENAI_TOKENS)
            return
        gemini_path = "/v1beta/models/gemini-2.5-flash:"
        if path in (gemini_path + "generateContent", gemini_path + "streamGenerateContent"):
            self._provider("gemini", GEMINI_TOKENS, model="gemini-2.5-flash", stream=path.endswith(":streamGenerateContent"))
            return
        if path not in ("/reset", "/hold", "/release"):
            self.close_connection = True
            self._json(404, {"error": "not found"})
            return
        parsed = self._body()
        if parsed is None:
            return
        _, body = parsed
        if path == "/reset":
            with lock:
                if gate is not None:
                    gate.release.set()
                gate = None
                calls.clear()
            self._json(200, {"ok": True})
        elif path == "/release":
            with lock:
                if gate is not None:
                    gate.release.set()
            self._json(200, {"ok": True})
        elif path == "/hold":
            account = body.get("account")
            match = body.get("match")
            status = body.get("status")
            if not isinstance(account, str) or not account or not isinstance(match, str) or type(status) is not int or status not in (200, 500):
                self._json(400, {"error": "hold requires account, match string, and status 200 or 500"})
                return
            with lock:
                conflict = gate is not None and not gate.release.is_set()
                if not conflict:
                    gate = Gate(account, match, status)
            if conflict:
                self._json(409, {"error": "a gate is already armed; release or reset it first"})
            else:
                self._json(200, {"ok": True})


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
    print("[mock-codex] control/OpenAI listening on :8080", flush=True)
    control.serve_forever()
