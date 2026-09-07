#!/usr/bin/env python3
"""Wait for Home and insert the gpt-4.1-mini price rule used by E2E."""

from __future__ import annotations

import json
import os
import sys
import time
import urllib.error
import urllib.request

HOME = os.environ.get("HOME_URL", "http://home:8327").rstrip("/")
MGMT = os.environ.get("MGMT_KEY", "e2e-mgmt-key")
MODEL = "gpt-4.1-mini"
PROVIDERS = [
    "openai",
    "openai-compatible",
    "openai-compatible-mock",
    "mock",
]


def call(method: str, path: str, body: dict | None = None):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(HOME + path, data=data, method=method)
    req.add_header("Authorization", "Bearer " + MGMT)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=10) as resp:
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


def wait_home(timeout: float = 90) -> None:
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        status, payload = call("GET", "/v0/management/billing/model-prices")
        if status == 200:
            print(f"Home ready: {payload}", flush=True)
            return
        last = (status, payload)
        print(f"waiting for Home management API: {last}", flush=True)
        time.sleep(2)
    raise SystemExit(f"Home never became ready: {last}")


def main() -> int:
    wait_home()
    for provider in PROVIDERS:
        status, payload = call(
            "POST",
            "/v0/management/billing/model-prices",
            {
                "provider": provider,
                "model": MODEL,
                "service_tier": "*",
                "min_input_tokens": 0,
                "input_price_per_million": 1000,
                "output_price_per_million": 0,
                "source": "e2e",
                "note": "cpa-key-quota docker e2e",
            },
        )
        print(f"seed {provider}/{MODEL}: HTTP {status} {payload}", flush=True)
        if status not in (200, 201) and not (
            isinstance(payload, dict)
            and "already" in json.dumps(payload).lower()
        ):
            # duplicate is acceptable on reruns
            if status not in (409, 422):
                print(f"WARN: unexpected seed status {status}", flush=True)
    status, payload = call("GET", "/v0/management/billing/model-prices")
    if status != 200:
        raise SystemExit(f"list prices failed: {status} {payload}")
    items = payload.get("items") if isinstance(payload, dict) else None
    if not items:
        raise SystemExit(f"Home price table empty after seed: {payload}")
    print(f"Home price table has {len(items)} rules", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
