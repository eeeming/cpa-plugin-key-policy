# Codex account binding E2E

Real `eceasy/cli-proxy-api:latest` host + `seakee/cpa-manager-plus:v1.13.1` +
the plugin built in `golang:1.23-bookworm` (C-ABI `.so`, glibc/bookworm like the
host image). Host config mirrors production: `routing.strategy=round-robin`,
`session-affinity=true`, `request-retry=0`, `commercial-mode=true`,
`ws-auth=true`, plugin priority 10, no home.

```bash
bash e2e/codex/run.sh            # KEEP_STACK=1 keeps containers for debugging
```

Upstream: two Codex OAuth auth files (`codex-a.json`, `codex-b.json`) whose
access tokens map to accounts A/B in `mock_codex.py`. The Codex executor
hard-codes `https://chatgpt.com/backend-api/codex/responses`, so the mock owns
the `chatgpt.com` / `auth.openai.com` network aliases and serves TLS with a
throwaway CA trusted only by the CPA container (`SSL_CERT_FILE`). The mock
records which account served every call and every header name it received.

Differences from real Codex: no real ChatGPT backend, no token refresh, no
upstream 429/quota headers, fixed usage (1000 input tokens per call), no
WebSocket (`/v1/ws`) transport.

`e2e_codex.py` covers: static binding, native scheduling for unbound keys,
live reassignment, session affinity on a multi-account binding, disabled
account → 503 / fallback, quota 429 + usage, forged internal header, SSE and
non-stream on `/v1/chat/completions` and `/v1/responses`, the `codex-auths`
catalog / pick counter, the UI resource, and Plus key + alias sync.
