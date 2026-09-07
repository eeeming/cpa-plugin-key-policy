# Docker E2E

Real HTTP stack: plugin `.so` + official CPA + **Home (Plus billing / model-prices)** + mock OpenAI-compat upstream.

```bash
make e2e-docker
# or: bash e2e/run.sh
```

Images: `eceasy/cli-proxy-api` (plugin host) + `eceasy/cli-proxy-api-home` (Plus price table). The community `cli-proxy-api-plus` image is private and is not pulled.

What it checks:

1. Unbound `api-keys` still reach upstream
2. Bound key is billed from Home `GET /v0/management/billing/model-prices`
3. Over daily USD → HTTP 429 `daily_exceeded` (no upstream call)
4. Unbound key still works after the bound key is blocked
5. Disabled policy is a no-op
6. RPM → HTTP 429 `rpm_exceeded`
