#!/usr/bin/env python3
"""Codex account binding E2E through a real CPA host (see README.md)."""

from __future__ import annotations

import hashlib
import json
import os
import sys
import time
import urllib.error
import urllib.request
import uuid

CPA = os.environ.get("CPA_URL", "http://cpa:8317").rstrip("/")
PLUS = os.environ.get("PLUS_URL", "http://plus:18317").rstrip("/")
MOCK = os.environ.get("MOCK_URL", "http://mock:8080").rstrip("/")
MGMT = os.environ.get("MGMT_KEY", "e2e-mgmt-key")
ADMIN = os.environ.get("PLUS_ADMIN_KEY", "e2e-admin-key")
PLUGIN = "cpa-key-quota"
BASE = f"/v0/management/plugins/{PLUGIN}"
MODEL = os.environ.get("CODEX_MODEL", "gpt-5.6-sol")
HEADER = "X-Cpa-Key-Quota-Binding"
AUTH = {"A": "codex-a.json", "B": "codex-b.json"}

results: list[tuple[str, bool, str]] = []
current = "setup"


def check(cond: bool, msg: str) -> bool:
    results.append((current, bool(cond), msg))
    print(f"{'OK  ' if cond else 'FAIL'} [{current}] {msg}", flush=True)
    return bool(cond)


def section(name: str) -> None:
    global current
    current = name
    print(f"\n===== {name} =====", flush=True)


def http(method, url, *, headers=None, body=None, timeout=30, raw_body=False):
    data = None if body is None else json.dumps(body).encode()
    req = urllib.request.Request(url, data=data, method=method)
    for k, v in (headers or {}).items():
        req.add_header(k, v)
    if data is not None:
        req.add_header("Content-Type", "application/json")
    try:
        with urllib.request.urlopen(req, timeout=timeout) as resp:
            raw = resp.read()
            status, ctype = resp.status, resp.headers.get("Content-Type", "")
    except urllib.error.HTTPError as e:
        raw, status, ctype = e.read(), e.code, e.headers.get("Content-Type", "")
    if raw_body:
        return status, raw.decode(errors="replace"), ctype
    try:
        return status, json.loads(raw.decode() or "null"), ctype
    except Exception:
        return status, raw.decode(errors="replace"), ctype


def mgmt(method, path, body=None, query=""):
    url = CPA + path + (("?" + query) if query else "")
    status, payload, _ = http(method, url, headers={"Authorization": "Bearer " + MGMT}, body=body)
    return status, payload


def plus(method, path, body=None):
    status, payload, _ = http(method, PLUS + path, headers={"Authorization": "Bearer " + ADMIN}, body=body)
    return status, payload


def calls() -> list[dict]:
    return http("GET", MOCK + "/calls")[1]["calls"]


def err_code(payload) -> str:
    if isinstance(payload, dict) and isinstance(payload.get("error"), dict):
        return str(payload["error"].get("code") or payload["error"].get("type") or "")
    return str(payload)[:200]


def send(key, kind="chat", stream=False, session=None, extra=None, prompt=None):
    """One downstream request; returns (status, payload, account served or None)."""
    prompt = prompt or f"ping {uuid.uuid4()}"
    headers = {"Authorization": "Bearer " + key}
    if session:
        headers["Session_id"] = session
    headers.update(extra or {})
    if kind == "chat":
        path = "/v1/chat/completions"
        body = {"model": MODEL, "messages": [{"role": "user", "content": prompt}], "stream": stream}
    else:
        path = "/v1/responses"
        body = {"model": MODEL, "input": prompt, "stream": stream}
    before = len(calls())
    status, payload, ctype = http("POST", CPA + path, headers=headers, body=body, raw_body=stream)
    after = calls()
    new = after[before:]
    account = new[-1]["account"] if new else None
    if status == 200:
        text = payload if stream else json.dumps(payload)
        if stream and "text/event-stream" not in ctype:
            check(False, f"{kind} stream response content-type {ctype!r}")
        if account and f"acct-{account}-" not in text:
            check(False, f"response body does not carry mock marker for account {account}: {text[:200]}")
    return status, payload, account


def run(key, n, label, **kw):
    accounts = []
    for i in range(n):
        kinds = [("chat", False), ("chat", True), ("responses", False), ("responses", True)]
        kind, stream = kinds[i % len(kinds)]
        status, payload, acct = send(key, kind=kind, stream=stream, **kw)
        if status != 200:
            check(False, f"{label}: {kind} stream={stream} HTTP {status} {str(payload)[:300]}")
        accounts.append(acct)
    print(f"  {label}: accounts={accounts}", flush=True)
    return accounts


def bind(key_id, plain, **fields):
    status, payload = mgmt("POST", BASE + "/keys", {"id": key_id, "name": key_id, "key": plain, **fields})
    check(status in (200, 201), f"bind {key_id} {fields} -> HTTP {status}")
    return payload


def patch(key_id, **fields):
    status, payload = mgmt("PATCH", BASE + "/keys", {"id": key_id, **fields})
    check(status == 200, f"patch {key_id} {fields} -> HTTP {status} {'' if status == 200 else payload}")
    return payload


def pick_count() -> int:
    return int(mgmt("GET", BASE + "/codex-auths")[1]["diagnostics"]["scheduler_pick_count"])


def set_auth_disabled(name, disabled):
    status, payload = mgmt("PATCH", "/v0/management/auth-files/status", {"name": name, "disabled": disabled})
    check(status == 200, f"auth-files/status {name} disabled={disabled} -> HTTP {status} {payload}")


def wait_ready(timeout=120):
    deadline = time.time() + timeout
    last = None
    while time.time() < deadline:
        try:
            status, payload = mgmt("GET", BASE + "/status")
            if status == 200 and isinstance(payload, dict) and payload.get("prices_available"):
                return
            last = (status, payload)
        except Exception as e:  # CPA still starting
            last = str(e)
        time.sleep(1.5)
    raise SystemExit(f"plugin not ready: {last}")


def seed_prices():
    body = {"prices": {MODEL: {"prompt": 1000, "completion": 0, "cache": 0, "cacheRead": 0,
                               "promptConfigured": True, "completionConfigured": True, "source": "e2e"}}}
    status, payload = plus("PUT", "/v0/management/model-prices", body)
    check(status in (200, 201), f"seed Plus price {MODEL} prompt=1000/M -> HTTP {status}")


def main() -> int:
    deadline = time.time() + 90
    while True:
        try:
            if plus("GET", "/health")[0] == 200:
                break
        except Exception:
            pass
        if time.time() > deadline:
            raise SystemExit("Plus not healthy")
        time.sleep(1)
    seed_prices()
    wait_ready()

    # ---------------------------------------------------------------- 8a
    section("8 catalog")
    status, cat = mgmt("GET", BASE + "/codex-auths")
    ids = sorted(a["id"] for a in (cat or {}).get("accounts", []))
    check(status == 200 and cat.get("available") is True and cat.get("source") == "host",
          f"GET codex-auths available/source=host: {status} {cat.get('source') if isinstance(cat, dict) else cat}")
    check(ids == sorted(AUTH.values()), f"codex-auths lists A,B: {ids}")
    picks0 = pick_count()

    # ---------------------------------------------------------------- 2
    section("2 static binding")
    bind("k1", "sk-k1", codex_auth_ids=[AUTH["A"]])
    bind("k2", "sk-k2", codex_auth_ids=[AUTH["B"]])
    a1 = run("sk-k1", 8, "k1->A")
    check(a1 and all(a == "A" for a in a1), f"k1 bound to A: all 8 on A ({a1})")
    a2 = run("sk-k2", 8, "k2->B")
    check(a2 and all(a == "B" for a in a2), f"k2 bound to B: all 8 on B ({a2})")
    a3 = run("sk-k3", 8, "k3 native")
    check(all(a in ("A", "B") for a in a3), f"k3 (no plugin policy) served natively: {a3}")
    check(set(a3) == {"A", "B"}, f"k3 native round-robin reached both accounts: {sorted(set(a3))}")

    # ---------------------------------------------------------------- 7
    section("7 stream / responses")
    for kind, stream in [("chat", False), ("chat", True), ("responses", False), ("responses", True)]:
        status, payload, acct = send("sk-k2", kind=kind, stream=stream)
        check(status == 200 and acct == "B", f"k2 {kind} stream={stream}: HTTP {status} on {acct}")
        if stream:
            check(isinstance(payload, str) and "data:" in payload, f"{kind} stream returned SSE frames")
    c = calls()
    check(all(x["stream"] is True for x in c), "Codex upstream always called with stream=true (non-stream is aggregated by CPA)")

    # ---------------------------------------------------------------- 3
    section("3 reassignment")
    patch("k1", codex_auth_ids=[AUTH["B"]])
    a = run("sk-k1", 6, "k1 after A->B")
    check(a and all(x == "B" for x in a), f"k1 new requests immediately on B: {a}")
    patch("k1", codex_auth_ids=[AUTH["A"]])
    a = run("sk-k1", 4, "k1 after B->A")
    check(a and all(x == "A" for x in a), f"k1 switched back to A: {a}")

    # ---------------------------------------------------------------- 4
    section("4 session affinity")
    bind("k4", "sk-k4", codex_auth_ids=[AUTH["A"], AUTH["B"]])
    sess = {}
    for sid in ("sess-one", "sess-two", "sess-three", "sess-four"):
        sess[sid] = run("sk-k4", 5, f"k4 {sid}", session=sid)
        check(len(set(sess[sid])) == 1 and sess[sid][0] in ("A", "B"), f"k4 {sid} 5 turns stuck to one account: {sess[sid]}")
    firsts = {v[0] for v in sess.values()}
    check(firsts == {"A", "B"}, f"k4 different sessions spread over A and B: {firsts}")
    nos = run("sk-k4", 6, "k4 no session")
    check(set(nos) <= {"A", "B"}, f"k4 without session stays inside A+B: {nos}")

    # ---------------------------------------------------------------- 5
    section("5 unavailable / fallback")
    bind("k5", "sk-k5", codex_auth_ids=[AUTH["A"]])
    status, _, acct = send("sk-k5")
    check(status == 200 and acct == "A", f"k5 baseline on A: {status} {acct}")
    set_auth_disabled(AUTH["A"], True)
    time.sleep(1.5)
    before = len(calls())
    status, payload, acct = send("sk-k5")
    check(status == 503, f"k5 bound A disabled, fallback off -> 503: got {status} {str(payload)[:300]}")
    check(err_code(payload) == "codex_binding_unavailable", f"503 code codex_binding_unavailable: {err_code(payload)}")
    check(len(calls()) == before, "503 never reached upstream")
    status, payload, _ = send("sk-k5", kind="chat", stream=True)
    check(status == 503, f"k5 stream with A disabled -> 503: got {status} {str(payload)[:200]}")
    status, _, acct = send("sk-k2")
    check(status == 200 and acct == "B", f"k2 (B) unaffected while A disabled: {status} {acct}")
    a = run("sk-k3", 4, "k3 while A disabled")
    check(a and all(x == "B" for x in a), f"native k3 only uses B while A disabled: {a}")
    patch("k5", codex_fallback=True)
    a = run("sk-k5", 4, "k5 fallback on")
    check(a and all(x == "B" for x in a), f"k5 fallback on -> served by B: {a}")
    set_auth_disabled(AUTH["A"], False)
    time.sleep(1.5)
    a = run("sk-k5", 3, "k5 A re-enabled")
    check(a and all(x == "A" for x in a), f"k5 back on A once re-enabled (fallback only when no match): {a}")

    # ---------------------------------------------------------------- 6
    section("6 quota & internal header")
    bind("quota", "sk-quota", rpm=50, daily_limit_usd=1.5, weekly_limit_usd=0, codex_auth_ids=[AUTH["B"]])
    for i in range(2):
        status, payload, acct = send("sk-quota")
        check(status == 200 and acct == "B", f"quota key request #{i + 1} admitted on B: {status} {acct}")
    daily = 0.0
    for _ in range(40):
        st, usage = mgmt("GET", BASE + "/keys/usage", query="id=quota")
        daily = float((usage or {}).get("daily_usd") or 0)
        if daily >= 1.99:
            break
        time.sleep(0.5)
    check(1.99 <= daily <= 2.01, f"usage billed 2 x $1.00 from Plus price: daily_usd={daily}")
    before = len(calls())
    status, payload, _ = send("sk-quota")
    check(status == 429 and err_code(payload) == "insufficient_quota", f"over daily cap -> 429 insufficient_quota: {status} {err_code(payload)}")
    status, payload, _ = send("sk-quota", kind="chat", stream=True)
    check(status == 429, f"over cap stream -> 429: {status}")
    check(len(calls()) == before, "429 never reached upstream")
    patch("k4", rpm=1)
    time.sleep(61)  # fresh RPM window for k4
    s1 = send("sk-k4")[0]
    s2, p2, _ = send("sk-k4")
    check(s1 == 200 and s2 == 429 and err_code(p2) == "rate_limit_exceeded", f"RPM=1 -> 200 then 429 rate_limit_exceeded: {s1} {s2} {err_code(p2)}")
    patch("k4", rpm=0)

    # Forged binding snapshots: garbage, well-formed-but-unsigned, case variant.
    forged_payload = "eyJ2ZXJzaW9uIjoxLCJrZXlfaWQiOiJ4IiwiaWRzIjpbImNvZGV4LWIuanNvbiJdLCJncm91cCI6IiIsImZhbGxiYWNrIjpmYWxzZSwicmVzdHJpY3RlZCI6dHJ1ZX0"
    forged = {"garbage": {HEADER: "garbage"},
              "unsigned-B": {HEADER: forged_payload + "." + "A" * 43},
              "lowercase": {HEADER.lower(): forged_payload + "." + "A" * 43}}
    for name, hdr in forged.items():
        status, payload, acct = send("sk-k1", extra=hdr)
        check(status == 200 and acct == "A", f"k1 (bound A) forged header [{name}] ignored, still A: {status} {acct}")
        status, payload, acct = send("sk-k3", extra=hdr)
        check(status == 200 and acct in ("A", "B"), f"k3 (no policy) forged header [{name}] cleared, native: {status} {acct}")
        status, payload, acct = send("sk-quota", extra=hdr)
        check(status == 429, f"quota key over cap + forged header [{name}] still 429: {status}")
    leaked = [x["n"] for x in calls() if HEADER.lower() in x["header_names"]]
    check(not leaked, f"no upstream call carried {HEADER} (checked {len(calls())} calls): leaked={leaked}")

    # ---------------------------------------------------------------- 8b
    section("8 diagnostics & UI")
    picks1 = pick_count()
    check(picks1 > picks0, f"scheduler_pick_count grew {picks0} -> {picks1}")
    status, html, ctype = http("GET", CPA + f"/v0/resource/plugins/{PLUGIN}/index.html", raw_body=True)
    check(status == 200 and "<html" in html.lower() and len(html) > 10000, f"management UI loads: HTTP {status} {ctype} {len(html)} bytes")
    status, keys = mgmt("GET", BASE + "/keys")
    rows = {k["id"]: k for k in (keys.get("keys") if isinstance(keys, dict) else keys) or []}
    check(rows.get("k1", {}).get("codex_auth_ids") == [AUTH["A"]], f"GET /keys shows k1 codex_auth_ids: {rows.get('k1', {}).get('codex_auth_ids')}")

    # ---------------------------------------------------------------- 9
    section("9 Plus sync")
    status, cur = plus("GET", "/v0/management/api-keys")
    existing = (cur or {}).get("api-keys") or []
    want = existing + ["sk-plus-alias-0001", "sk-plus-noalias-0002"]
    status, payload = plus("PUT", "/v0/management/api-keys", want)
    if status != 200:
        status, payload = mgmt("PUT", "/v0/management/api-keys", want)
    check(status == 200, f"add 2 api-keys through Plus/CPA management: {status} {payload}")
    alias_hash = hashlib.sha256(b"sk-plus-alias-0001").hexdigest()
    status, payload = plus("PUT", "/v0/management/api-key-aliases", {"items": [{"apiKeyHash": alias_hash, "alias": "bob-team"}]})
    check(status == 200, f"Plus alias bob-team for sk-plus-alias-0001: {status}")
    time.sleep(2)
    status, res = mgmt("POST", BASE + "/keys/sync")
    check(status == 200 and isinstance(res, dict) and res.get("added", 0) >= 2, f"POST /keys/sync: {status} {res}")
    status, keys = mgmt("GET", BASE + "/keys")
    rows = (keys.get("keys") if isinstance(keys, dict) else keys) or []
    alias_row = next((r for r in rows if r.get("key_preview", "").startswith("sk-plus") and "0001" in r.get("key_preview", "")), None)
    noalias_row = next((r for r in rows if r.get("key_preview", "").startswith("sk-plus") and "0002" in r.get("key_preview", "")), None)
    check(alias_row is not None and noalias_row is not None, f"synced rows present: {alias_row and alias_row.get('id')} {noalias_row and noalias_row.get('id')}")
    if alias_row and noalias_row:
        check(alias_row.get("name") == "bob-team", f"aliased key named from Plus alias: {alias_row.get('name')!r}")
        check(noalias_row.get("name") == noalias_row.get("key_preview"), f"no-alias key identified by its (masked) key: {noalias_row.get('name')!r}")
        patch(alias_row["id"], codex_auth_ids=[AUTH["A"]])
        patch(noalias_row["id"], codex_auth_ids=[AUTH["B"]])
        a = run("sk-plus-alias-0001", 4, "synced alias key -> A")
        check(a and all(x == "A" for x in a), f"synced aliased key bound to A: {a}")
        a = run("sk-plus-noalias-0002", 4, "synced no-alias key -> B")
        check(a and all(x == "B" for x in a), f"synced no-alias key bound to B: {a}")
        status, res = mgmt("POST", BASE + "/keys/sync")
        check(status == 200, f"re-sync: {status} {res}")
        a = run("sk-plus-alias-0001", 2, "aliased after re-sync")
        check(a and all(x == "A" for x in a), f"binding survives re-sync: {a}")
        # Alias added later in Plus: an auto-named key picks it up, binding kept.
        noalias_hash = hashlib.sha256(b"sk-plus-noalias-0002").hexdigest()
        status, _ = plus("PUT", "/v0/management/api-key-aliases", {"items": [
            {"apiKeyHash": alias_hash, "alias": "bob-team"}, {"apiKeyHash": noalias_hash, "alias": "carol-late"},
            {"apiKeyHash": hashlib.sha256(b"sk-k1").hexdigest(), "alias": "alice-plus"}]})
        check(status == 200, f"Plus alias carol-late added later: {status}")
        status, res = mgmt("POST", BASE + "/keys/sync")
        check(status == 200, f"re-sync after late alias: {status} {res}")
        status, keys = mgmt("GET", BASE + "/keys")
        rows = {r["id"]: r for r in ((keys.get("keys") if isinstance(keys, dict) else keys) or [])}
        late = rows.get(noalias_row["id"], {})
        check(late.get("name") == "carol-late", f"auto-named key renamed from late Plus alias: {late.get('name')!r}")
        check(late.get("codex_auth_ids") == [AUTH["B"]], f"binding kept after rename: {late.get('codex_auth_ids')}")
        check(rows.get("k1", {}).get("name") == "k1", f"custom name k1 not overwritten: {rows.get('k1', {}).get('name')!r}")
        a = run("sk-plus-noalias-0002", 2, "renamed key")
        check(a and all(x == "B" for x in a), f"renamed key still on B: {a}")

    # ---------------------------------------------------------------- final
    section("final")
    leaked = [x["n"] for x in calls() if HEADER.lower() in x["header_names"]]
    check(not leaked, f"final: none of {len(calls())} upstream calls carried {HEADER}")
    check(pick_count() > picks1, f"pick count still growing: {pick_count()}")
    by_acct = {}
    for x in calls():
        by_acct[x["account"]] = by_acct.get(x["account"], 0) + 1
    print(f"upstream calls by account: {by_acct}", flush=True)

    print("\n---- summary ----", flush=True)
    sections = {}
    for sec, ok, _ in results:
        sections.setdefault(sec, []).append(ok)
    for sec, oks in sections.items():
        print(f"{'PASS' if all(oks) else 'FAIL'} {sec}: {sum(oks)}/{len(oks)}", flush=True)
    failed = [r for r in results if not r[1]]
    for sec, _, msg in failed:
        print(f" - [{sec}] {msg}", flush=True)
    print("all codex e2e checks passed" if not failed else f"{len(failed)} checks failed", flush=True)
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
