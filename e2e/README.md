# Docker E2E

Real HTTP stack: plugin `.so` + official CPA + mock Plus prices + mock OpenAI-compat upstream.

```bash
make e2e-docker
# or: bash e2e/run.sh
```

What it checks against a live `eceasy/cli-proxy-api` container:

1. Unbound `api-keys` still reach upstream
2. Bound key is billed from Plus `GET /billing/model-prices`
3. Over daily USD → HTTP 429 `daily_exceeded` (no upstream call)
4. Unbound key still works after the bound key is blocked
5. Disabled policy is a no-op
6. RPM → HTTP 429 `rpm_exceeded`
