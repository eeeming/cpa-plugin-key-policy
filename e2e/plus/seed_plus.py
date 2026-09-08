#!/usr/bin/env python3
"""Wait for CPA-Manager-Plus and PUT the gpt-4.1-mini e2e price."""

from __future__ import annotations

import json
import os
import sys
import time
import urllib.error
import urllib.request

PLUS = os.environ.get("PLUS_URL", "http://plus:18317").rstrip("/")
ADMIN = os.environ.get("PLUS_ADMIN_KEY", "e2e-admin-key")
MODEL = "gpt-4.1-mini"


def call(method: str, path: str, body: dict | None = None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(PLUS + path, data=data, method=method)
    req.add_header("Authorization", "Bearer " + ADMIN)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=15) as resp:
            raw = resp.read()
            return resp.status, json.loads(raw.decode() or "null") if raw else None
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            parsed = json.loads(raw.decode() or "null")
        except Exception:
            parsed = raw.decode(errors="replace")
        return e.code, parsed
    except Exception as e:
        return 0, str(e)


def wait_plus(timeout: float = 90) -> None:
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        status, payload = call("GET", "/health")
        if status == 200:
            print(f"Plus health: {payload}", flush=True)
            return
        last = (status, payload)
        print(f"waiting for manager-plus: {last}", flush=True)
        time.sleep(2)
    raise SystemExit(f"manager-plus never became ready: {last}")


def main() -> int:
    wait_plus()
    body = {
        "prices": {
            MODEL: {
                "prompt": 1000,
                "completion": 0,
                "cache": 0,
                "cacheRead": 0,
                "promptConfigured": True,
                "completionConfigured": True,
                "source": "e2e",
            }
        }
    }
    status, payload = call("PUT", "/v0/management/model-prices", body)
    print(f"PUT model-prices: HTTP {status} {payload}", flush=True)
    if status not in (200, 201):
        raise SystemExit(f"seed failed: {status} {payload}")
    status, payload = call("GET", "/v0/management/model-prices")
    if status != 200 or not isinstance(payload, dict):
        raise SystemExit(f"list prices failed: {status} {payload}")
    prices = payload.get("prices") or {}
    row = prices.get(MODEL) if isinstance(prices, dict) else None
    if not isinstance(row, dict) or float(row.get("prompt") or 0) != 1000:
        raise SystemExit(f"seeded price missing: {payload}")
    print(f"Plus price table has {len(prices)} models, {MODEL} prompt=1000", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
