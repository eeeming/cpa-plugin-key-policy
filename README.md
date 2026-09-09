# cpa-key-quota

Per-key USD and RPM quota plugin for [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI).

It does **not** issue keys and does **not** replace Plus authentication. You bind keys that already exist in Plus `api-keys`. USD amounts are billed from the **CPA-Manager-Plus** price table (this plugin has no prices of its own). Bound, enabled policies that exceed daily USD, weekly USD, or RPM return HTTP 429. Unbound keys and disabled policies are no-ops (Plus still serves them).

| | |
|---|---|
| **Plugin ID** | `cpa-key-quota` |
| **License** | MIT |
| **Chinese** | [README.zh-CN.md](./README.zh-CN.md) |

## Requirements

- CLIProxyAPI v7 plugin host that can `dlopen` a c-shared `.so`. Use a glibc image such as `eceasy/cli-proxy-api`. Alpine / `CGO_ENABLED=0` hosts cannot load this plugin.
- Linux `amd64` or `arm64` matching the CPA host (`CGO_ENABLED=1`).
- **CPA-Manager-Plus** for USD limits. The plugin reads Plus `GET /v0/management/model-prices` and bills with those rates. RPM still works if Plus is missing.

Do **not** declare `frontend_auth_provider`. Over-quota must `Terminate` with 429, not `Authenticated: false`.

## Deploy

The plugin is a `.so` that CPA `dlopen`s. Typical layout: CPA and CPA-Manager-Plus on the same Docker network; the `.so` is bind-mounted into CPA’s `plugins/` directory.

### 1. Build the `.so`

On a Linux host of the same arch as CPA:

```bash
make build-linux-amd64   # or: make build-linux-arm64
```

Output: `dist/cpa-key-quota_linux_<arch>.so`.

From macOS (or any machine that is not the CPA arch), cross-compile with Docker. CPA on x86_64 needs `linux/amd64` even if your laptop is ARM:

```bash
docker build --platform linux/amd64 -f e2e/Dockerfile.plugin -t cpa-key-quota-so:amd64 .
cid=$(docker create --platform linux/amd64 cpa-key-quota-so:amd64)
mkdir -p dist
docker cp "$cid":/cpa-key-quota.so dist/cpa-key-quota_linux_amd64.so
docker rm "$cid"
file dist/cpa-key-quota_linux_amd64.so   # must say: ELF 64-bit LSB shared object, x86-64
```

Use `--platform linux/arm64` and `cpa-key-quota_linux_arm64.so` on ARM CPA hosts. Do not copy an ARM `.so` onto an amd64 host (or the reverse): CPA will fail to load it.

### 2. Install the file

Copy it to the directory CPA loads (`plugins` in `config.yaml`, often mounted at `/CLIProxyAPI/plugins`):

```text
plugins/linux/amd64/cpa-key-quota.so
```

ARM hosts: `plugins/linux/arm64/cpa-key-quota.so`.

### 3. Point the plugin at CPA-Manager-Plus

Add the snippet in [Configure](#configure) to CPA `config.yaml`. `plus_base_url` must be the Plus base URL **as the CPA container sees it** (Docker DNS, not a public hostname), for example `http://cpa-manager-plus:18317`. `plus_management_key` is the **Plus admin key** (`CPA_MANAGER_ADMIN_KEY`), used to read the price table and, for **Sync**, Plus `api-keys`. It is not the CPA `remote-management.secret-key`.

USD limits do not work until this is set and Plus has model prices. See [Prices](#prices).

### 4. Restart CPA

The plugins directory is often mounted read-only. Replacing the `.so` is not enough; restart the CPA process so it `dlopen`s the new file:

```bash
docker restart cli-proxy-api
```

Plus and any reverse proxy do not need a restart for a plugin-only update.

### 5. Verify

With the CPA management key:

```http
GET /v0/management/plugins
GET /v0/management/plugins/cpa-key-quota/status
```

Expect `cpa-key-quota` `registered: true`, `effective_enabled: true`, and `prices_available: true` once Plus returns a table. CPA logs should show `plugin loaded plugin_id=cpa-key-quota path=plugins/linux/<arch>/cpa-key-quota.so`.

The management UI is embedded at `/v0/resource/plugins/cpa-key-quota/index.html` (Plus panel menu **Key Quota**).

To ship a new build later: rebuild, overwrite the same `.so` path, restart CPA only.

## Prices

**USD billing depends on CPA-Manager-Plus.** This plugin does not ship, cache-edit, or serve a price table. Edit rates in Plus; the plugin only reads them.

| | |
|---|---|
| **Required service** | CPA-Manager-Plus (`seakee/cpa-manager-plus` in a typical compose) |
| **API** | `GET {plus_base_url}/v0/management/model-prices` with Bearer `plus_management_key` |
| **Fallback** | If that path is HTTP 404, `GET …/v0/management/billing/model-prices` (Home / older Plus) |
| **Without Plus** | Empty `plus_base_url`, or no table yet: USD gates stay off. RPM still works. |

Prices are cached for 30 seconds. A later fetch failure keeps the last successful table so USD limits do not fail open. There is no hardcoded model family (for example no special-case GPT-5/6 rates): a name is billed as soon as Plus lists it.

## Configure

See [`config.example.yaml`](config.example.yaml). Minimal `config.yaml` fragment:

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cpa-key-quota:
      enabled: true
      priority: 10
      state_file: "cpa-key-quota-state.json"
      plus_base_url: "http://cpa-manager-plus:18317"
      plus_management_key: "your-plus-admin-key"
```

| Field | Purpose |
|---|---|
| `enabled` | Pause the plugin without unloading it |
| `state_file` | JSON file for bound policies and usage (created if missing; chmod 600) |
| `plus_base_url` | CPA-Manager-Plus base URL (Docker-internal recommended). See [Prices](#prices) |
| `plus_management_key` | Plus admin Bearer for the price API and for **Sync** (`GET /v0/management/api-keys`) |
| `keys` | Optional seed list. After the state file exists, the file wins |

Usage is kept in memory and flushed to the state file about every 15s (atomic temp + rename).

## Bind and sync

**Bind** (management UI **Add**, or API): submit the existing Plus key **plaintext once**. The plugin stores `sha256:` + a truncated preview + CPA `caller_scope`. Plaintext is never written to disk or returned in list/status JSON.

```http
POST /v0/management/plugins/cpa-key-quota/keys
Authorization: Bearer <CPA remote-management secret-key>
Content-Type: application/json

{"id":"k-team","name":"team","key":"sk-...","daily_limit_usd":0.5,"weekly_limit_usd":0,"rpm":0}
```

`0` on a limit means unlimited. PATCH the same path to edit limits, rename, enable/disable, or rotate the key (new plaintext, hashed immediately).

**Sync from Plus** (**Sync**): `POST /v0/management/plugins/cpa-key-quota/keys/sync` fetches Plus `GET /v0/management/api-keys` and binds keys that are not already hashed in the plugin. Existing names/limits/enabled flags are not overwritten. New keys start enabled with unlimited USD/RPM. Plus currently returns a plaintext string list **without display names**, so synced names default to the preview; rename in **Edit**.

Disable (`enabled: false`) pauses quota for that key; the Plus key still works. Unbind removes the policy only. Deleting the key from Plus `api-keys` is what produces 401.

**Inspect and reset:** `GET /v0/management/plugins/cpa-key-quota/keys/usage?id=<id>` returns one key's 24h/7d USD, call counts, window start/reset times, and the per-model breakdown the card shows. `POST /v0/management/plugins/cpa-key-quota/keys/reset-windows` with `{"ids":["k-a","k-b"]}` zeroes the rolling 24h/7d usage and the RPM minute bucket for those keys; limit numbers, names, and enabled flags are unchanged, and Plus usage is not touched. Unknown ids are reported in `failed`.

## Rolling windows

Daily and weekly USD limits are **rolling**, not calendar days/weeks:

- Daily: 24 hours from that key’s first billed request in the window, then the bucket resets
- Weekly: 7×24 hours from first bill in the window, then the bucket resets

The next reset time is shown on each key card. Unused keys show “starts at first bill”. The list is ordered by last request (most recent first).

## Client errors

HTTP **429**, OpenAI-shaped `error` object:

| Gate | `error.type` / `error.code` | Notes |
|---|---|---|
| Daily or weekly USD | `insufficient_quota` | Message mentions daily vs weekly |
| RPM | `rate_limit_exceeded` | `Retry-After: 60` |

Unbound and disabled keys are not 429’d by this plugin.

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

## Privacy

- **Stored per key:** `sha256:` of the secret, truncated preview, `caller_scope`, limits, usage. Not the secret.
- **Management UI session:** CPA management key is kept in memory only (tab refresh logs you out). If the UI is iframed in the official panel with “remember password”, it may reuse the panel’s existing `localStorage` blob; this plugin never writes that key itself.
- **Plus sync/prices:** the Plus admin key in `plus_management_key` can list plaintext `api-keys`. Treat it as a host secret. The plugin hashes in memory and does not persist those strings. Prefer an internal HTTPS or Docker-network `plus_base_url` (loopback `http://` is typical in compose). A public cleartext URL would expose the admin key and every api-key on the wire.
- **State file:** any path the CPA process can write (operator-controlled). Mode 600. Do not commit it.
- **Management API:** mutating `/v0/management/plugins/cpa-key-quota/*` is authenticated by **CPA**, not by the plugin. Do not expose those routes without the host secret-key.
- **UI framing:** the embedded page sends `Content-Security-Policy: frame-ancestors 'self'` and only reuses the panel’s remembered key when the parent frame is same-origin.
- **Prices down:** a failed Plus fetch keeps the last successful price table so USD limits do not fail open. No table yet ⇒ USD gate off, RPM still on.

Accepted operator risk: Plus’s own `GET /v0/management/api-keys` returns plaintext. This plugin cannot change that API.

## Tests

```bash
make test-all    # Go tests + frontend tests (npm ci && npm test)
go test -race ./...            # what CI runs for Go
cd web && npm run typecheck    # tsc --noEmit
```

Docker stacks: `make e2e-docker` (Home prices) and `make e2e-plus` (CPA-Manager-Plus). Details: [e2e/README.md](e2e/README.md).
