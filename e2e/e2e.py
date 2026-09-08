#!/usr/bin/env python3
"""Real HTTP E2E against CPA + cpa-key-quota plugin inside Docker."""

from __future__ import annotations

import json
import os
import sys
import time
import urllib.error
import urllib.request

CPA = os.environ.get("CPA_URL", "http://cpa:8317").rstrip("/")
HOME = os.environ.get("HOME_URL", "http://home:8327").rstrip("/")
MGMT = os.environ.get("MGMT_KEY", "e2e-mgmt-key")
PLUGIN = "cpa-key-quota"
MODEL = "gpt-4.1-mini"

BOUND = "sk-bound"
UNBOUND = "sk-unbound"
RPM_KEY = "sk-rpm"
DISABLED_KEY = "sk-disabled"

failures: list[str] = []


def fail(msg: str) -> None:
    failures.append(msg)
    print(f"FAIL: {msg}", flush=True)


def ok(msg: str) -> None:
    print(f"OK:   {msg}", flush=True)


def http(method: str, url: str, *, headers: dict[str, str] | None = None, body: dict | None = None, timeout: float = 20):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(url, data=data, method=method)
    for k, v in (headers or {}).items():
        req.add_header(k, v)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read()
            parsed = json.loads(raw.decode() or "null") if raw else None
            return resp.status, parsed, raw
    except urllib.error.HTTPError as e:
        raw = e.read()
        try:
            parsed = json.loads(raw.decode() or "null")
        except Exception:
            parsed = raw.decode(errors="replace")
        return e.code, parsed, raw


def mgmt(method: str, path: str, body: dict | None = None, query: str = ""):
    url = CPA + path + (("?" + query) if query else "")
    return http(method, url, headers={"Authorization": "Bearer " + MGMT}, body=body)


def chat(api_key: str):
    return http(
        "POST",
        CPA + "/v1/chat/completions",
        headers={"Authorization": "Bearer " + api_key},
        body={"model": MODEL, "messages": [{"role": "user", "content": "ping"}]},
    )


def error_code(payload) -> str:
    if isinstance(payload, dict):
        err = payload.get("error")
        if isinstance(err, dict):
            return str(err.get("code") or "")
        if isinstance(err, str):
            return err
    return ""


def home(method: str, path: str, body: dict | None = None):
    return http(method, HOME + path, headers={"Authorization": "Bearer " + MGMT}, body=body)


def wait_cpa(timeout: float = 90) -> None:
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        status, payload, raw = mgmt("GET", f"/v0/management/plugins/{PLUGIN}/status")
        if status == 200 and isinstance(payload, dict) and payload.get("prices_available"):
            ok(f"plugin status {payload}")
            return
        last = (status, payload, raw)
        time.sleep(1.5)
    raise SystemExit(f"CPA/plugin never became ready with Home prices: last={last}")


def usage_daily(key_id: str) -> float:
    status, payload, _ = mgmt("GET", f"/v0/management/plugins/{PLUGIN}/keys/usage", query=f"id={key_id}")
    if status != 200 or not isinstance(payload, dict):
        fail(f"usage for {key_id}: HTTP {status} {payload}")
        return -1
    return float(payload.get("daily_usd") or 0)


def wait_daily(key_id: str, minimum: float, timeout: float = 20) -> float:
    deadline = time.time() + timeout
    got = 0.0
    while time.time() < deadline:
        got = usage_daily(key_id)
        if got + 1e-9 >= minimum:
            return got
        time.sleep(0.5)
    return got


def bind(key_id: str, plain: str, **limits):
    body = {"id": key_id, "name": key_id, "key": plain, **limits}
    status, payload, raw = mgmt("POST", f"/v0/management/plugins/{PLUGIN}/keys", body)
    if status not in (200, 201):
        fail(f"bind {key_id}: HTTP {status} {payload} {raw[:200]!r}")
        return
    ok(f"bound {key_id}")


def patch(key_id: str, **fields):
    body = {"id": key_id, **fields}
    status, payload, _ = mgmt("PATCH", f"/v0/management/plugins/{PLUGIN}/keys", body)
    if status != 200:
        fail(f"patch {key_id}: HTTP {status} {payload}")
        return
    ok(f"patched {key_id} {fields}")


def assert_home_prices() -> None:
    status, payload, _ = home("GET", "/v0/management/billing/model-prices")
    if status != 200 or not isinstance(payload, dict):
        fail(f"Home model-prices HTTP {status} {payload}")
        return
    items = payload.get("items") or []
    priced = [
        row
        for row in items
        if isinstance(row, dict)
        and row.get("model") == MODEL
        and float(row.get("input_price_per_million") or 0) == 1000
    ]
    if not priced:
        fail(f"Home price table missing gpt-4.1-mini @ 1000/M: {payload}")
        return
    ok(f"Home price table has {len(priced)} matching rules (schema={payload.get('price_rule_schema_version')})")


def main() -> int:
    print(f"E2E against CPA={CPA} Home={HOME}", flush=True)
    wait_cpa()
    assert_home_prices()

    # 1) Unbound key must reach the mock upstream.
    status, payload, _ = chat(UNBOUND)
    if status != 200:
        fail(f"unbound chat expected 200, got {status} {payload}")
    else:
        ok("unbound key reaches upstream")

    # 2) Bind the quota key. 1000 input tokens @ $1000/M = $1.00; daily cap $1.50.
    bind("bound", BOUND, rpm=50, daily_limit_usd=1.5, weekly_limit_usd=0)

    status, payload, _ = chat(BOUND)
    if status != 200:
        fail(f"bound under-limit #1 expected 200, got {status} {payload}")
    else:
        ok("bound key admitted (1st)")
    daily = wait_daily("bound", 0.9)
    if daily < 0.9:
        fail(f"plugin did not bill first request, daily_usd={daily}")
    else:
        ok(f"first request billed daily_usd={daily}")

    status, payload, _ = chat(BOUND)
    if status != 200:
        fail(f"bound under-limit #2 expected 200, got {status} {payload}")
    else:
        ok("bound key admitted (2nd)")
    daily = wait_daily("bound", 1.9)
    if daily < 1.9:
        fail(f"plugin did not bill second request, daily_usd={daily}")
    else:
        ok(f"second request billed daily_usd={daily}")

    status, payload, _ = chat(BOUND)
    if status != 429:
        fail(f"over daily cap expected 429, got {status} {payload}")
    else:
        code = error_code(payload)
        if code != "insufficient_quota":
            fail(f"429 code={code!r}, want insufficient_quota payload={payload}")
        else:
            ok("over-limit returns 429 insufficient_quota")

    # 3) Unbound still works after a bound key is blocked.
    status, payload, _ = chat(UNBOUND)
    if status != 200:
        fail(f"unbound after 429 expected 200, got {status} {payload}")
    else:
        ok("unbound key still works after bound key is blocked")

    # 4) Disabled policy is a no-op (Plus still serves the key).
    bind("disabled", DISABLED_KEY, enabled=False, rpm=1, daily_limit_usd=0.01)
    status, payload, _ = chat(DISABLED_KEY)
    if status != 200:
        fail(f"disabled policy expected 200, got {status} {payload}")
    else:
        ok("disabled policy is no-op")
    if usage_daily("disabled") > 0:
        fail("disabled policy must not record USD")
    else:
        ok("disabled policy recorded no USD")

    # 5) RPM still enforced when we want it.
    bind("rpm", RPM_KEY, rpm=1, daily_limit_usd=0)
    status, payload, _ = chat(RPM_KEY)
    if status != 200:
        fail(f"rpm first request expected 200, got {status} {payload}")
    status, payload, _ = chat(RPM_KEY)
    if status != 429:
        fail(f"rpm second request expected 429, got {status} {payload}")
    else:
        code = error_code(payload)
        if code != "rate_limit_exceeded":
            fail(f"rpm 429 code={code!r}, want rate_limit_exceeded payload={payload}")
        else:
            ok("rate_limit_exceeded 429")

    print("---- summary ----", flush=True)
    if failures:
        for item in failures:
            print(f" - {item}", flush=True)
        return 1
    print("all e2e checks passed", flush=True)
    return 0


if __name__ == "__main__":
    sys.exit(main())
