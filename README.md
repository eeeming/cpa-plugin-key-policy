# cpa-key-quota

Downstream **per-key USD quota** plugin for [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) / Plus.

It binds keys that already live in Plus `api-keys`. Plus still authenticates and routes. This plugin only:

1. **Bills** bound keys using the Plus model-price table
2. **Blocks** over RPM / daily / weekly USD with HTTP 429

Unbound and disabled policies are no-ops.

| | |
|---|---|
| **Plugin ID** | `cpa-key-quota` |
| **License** | MIT |
| **中文说明** | [README.zh-CN.md](./README.zh-CN.md) |

## Request path

```
client key
  → Plus api-keys auth
  → request.intercept_before
       ├ not bound / policy disabled → no-op
       └ bound and enabled
            ├ over RPM / daily / weekly USD → Terminate 429
            └ else admit; usage.handle bills from Plus prices
```

Do **not** declare `frontend_auth_provider`. Over-quota must Terminate, not `Authenticated: false`.

## Bind

POST `/v0/management/plugins/cpa-key-quota/keys` with the existing plaintext key once. Only `sha256` hash + preview are stored.

Disable (`enabled: false`) pauses the quota without deleting the Plus key.

## Prices

USD comes from Plus `GET /v0/management/billing/model-prices` (cached). Matching: exact `service_tier` then `*`, then greatest `min_input_tokens` ≤ input. If that API is missing, RPM still works and USD limits do not fire. Edit prices in Plus, not in this plugin.

## Config

See `config.example.yaml`. Default state file: `cpa-key-quota-state.json`.

## Docker E2E

```bash
make e2e-docker
```

Builds the linux `.so`, starts official CPA and Home (Plus `billing/model-prices`), plus a mock OpenAI-compat upstream, then hits real `/v1/chat/completions`. Details: [e2e/README.md](./e2e/README.md).
