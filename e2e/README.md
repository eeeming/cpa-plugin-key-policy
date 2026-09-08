# Docker E2E

Two stacks. Production USD billing uses **CPA-Manager-Plus** `GET /v0/management/model-prices`; Home is only the fallback path in the plugin.

```bash
make e2e-plus     # CPA + CPA-Manager-Plus (the price source operators should run)
make e2e-docker   # CPA + Home billing/model-prices (404-fallback)
```

Images: `eceasy/cli-proxy-api` (plugin host) + `seakee/cpa-manager-plus` (`e2e-plus`) or `eceasy/cli-proxy-api-home` (`e2e-docker`). The community `cli-proxy-api-plus` image is private and is not pulled.

How to build and install the `.so` on a real CPA host is in the root [README.md](../README.md#deploy) / [README.zh-CN.md](../README.zh-CN.md#部署).

What both stacks check:

1. Unbound `api-keys` still reach upstream
2. Bound key is billed from the stack’s price API (`e2e-plus`: Plus `GET /v0/management/model-prices`; `e2e-docker`: Home `GET /v0/management/billing/model-prices`)
3. Over daily USD → HTTP 429 `insufficient_quota` (no upstream call)
4. Unbound key still works after the bound key is blocked
5. Disabled policy is a no-op
6. RPM → HTTP 429 `rate_limit_exceeded`

Plugin id is `cpa-key-quota`. E2E keys (`sk-bound`, `sk-unbound`, `sk-rpm`, `sk-disabled`) and management secrets (`e2e-mgmt-key`, `e2e-admin-key`) are synthetic. Do not commit live cloudflared credentials; `e2e/plus/gomami-cloudflared-config.yml` is gitignored.
