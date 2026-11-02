# Codex account binding E2E

Real `eceasy/cli-proxy-api:latest` host + `seakee/cpa-manager-plus:v1.13.1` +
the locally built plugin (C-ABI `.so`, glibc/bookworm like the host image).
The normal host configuration uses `routing.strategy=round-robin`,
`session-affinity=true`, `request-retry=0`, `commercial-mode=true`,
`ws-auth=true`, plugin priority 10, and no Home dispatch.

```bash
bash e2e/codex/run.sh
KEEP_STACK=1 bash e2e/codex/run.sh  # retain this stack for local browser screenshots
```

The runner accepts only local Unix-socket or loopback TCP Docker endpoints.
Initial teardown and the default EXIT trap target only the explicit Compose
project `cpa-key-quota-codex-e2e`; they never stop unrelated containers. The
trap preserves the build/test/timeout failure status and removes the project's
volumes and generated `.run` fixtures unless `KEEP_STACK=1`. `E2E_TIMEOUT`
(default 900 seconds) bounds each Compose operation and the E2E container wait.
A failed check or aborted scenario exits nonzero and still prints a summary.

## Local UI and explicit cleanup

With `KEEP_STACK=1`, open:

<http://127.0.0.1:18517/v0/resource/plugins/cpa-key-quota/index.html>

The CPA management fixture key is `e2e-mgmt-key`; enter it if the UI asks.
Port 18517 is already published on loopback only. Plus is internal on
`http://plus:18317`; no additional published ports or SSH forwarding are needed.
After screenshots/debugging, run from the repository root:

```bash
docker compose -p cpa-key-quota-codex-e2e -f e2e/codex/docker-compose.yml down -v --remove-orphans
rm -rf e2e/codex/.run
```

## Local native upstreams

The real host executors, request interceptors, model aliases, and payload rules
run unchanged. Only provider responses are mocked:

- Codex OAuth files `codex-a.json` / `codex-b.json` carry tokens for accounts A/B.
  The native Codex executor calls the mock's HTTPS
  `chatgpt.com/backend-api/codex/responses` using the actual TLS/executor path.
- OpenAI compatibility: `e2e-openai` resolves to native `e2e-openai-native`,
  using the local HTTP `/v1/chat/completions` and `sk-upstream-openai`.
- Claude OAuth: `claude-a.json` carries `tok-claude-a`; `e2e-claude` resolves to
  `claude-sonnet-4-5-20250929` and calls local HTTPS `api.anthropic.com/v1/messages`.
- Gemini API-key: `e2e-gemini` resolves to `gemini-2.5-flash`, using local HTTP
  `/v1beta/models/...:generateContent` and `tok-gemini-a`.

The mock owns the `chatgpt.com`, `auth.openai.com`, and `api.anthropic.com`
Compose network aliases. A throwaway CA/leaf includes those names and is trusted
only by this CPA container through `SSL_CERT_FILE`. No provider credentials or
remote servers are used. The mock records the provider, account, upstream model,
request body, and received header names for each authenticated provider call.
Usage is fixed at 1000 input tokens and zero output tokens per successful call.
There is no real token refresh, upstream quota accounting, or WebSocket coverage.

## Scenarios and reporting

All original meaningful scenarios remain: static bindings and native unbound
scheduling; live reassignment; session affinity; disabled-account 503 and
fallback; daily USD usage/cap and RPM rejection; forged internal headers; SSE
and non-stream `/v1/chat/completions` / `/v1/responses`; host account catalog;
management UI loading; and Plus key/alias synchronization preserving bindings.
Scheduler diagnostics assertions use observed callback count/time only, not
unknown scheduler configuration or log wording.

The batch API is
`POST /v0/management/plugins/cpa-key-quota/keys/codex-binding/batch`.
Cases cover set/add/remove, explicit `set []`, keep-mode group/fallback updates,
clear-all, normalized values, complete before/after response bindings, dry-run
without published changes, preview-before apply, and stale-preview 409. A valid
key followed by a missing or malformed key, duplicate/empty/untrimmed IDs,
malformed fields, incompatible clear operations, unknown quota fields, and
incomplete/duplicate/extra expected entries must fail atomically. Every failure
checks both prior bindings and subsequent A/B routing.

Dedicated `sk-routing-bound` / `sk-routing-unbound` policies have no RPM/daily/
weekly caps. Both request each non-Codex provider to prove native account/model
selection with the plugin loaded; each request includes a forged internal
binding header, which must never reach any provider. Native Codex OAuth alias
`e2e-codex-alias` is checked against the upstream resolved model and bound account.
Bound and unbound Codex calls also assert the host-applied payload override
`metadata.e2e_payload=codex-override` in the actual upstream body.

The final traffic-changing case temporarily enables host retries, holds an old
A+B request on A, batch-updates the allocation to A-only, and confirms a new
request reaches A before releasing the old attempt as HTTP 500. The old request
must retry B using its old signed snapshot, despite B being excluded from the
new allocation. `finally` releases the mock gate, waits for the request, restores
host retry settings, and restores the original binding. If the host does not
support temporary retry management, output explicitly reports the limitation
and exercises an in-flight hold-success allocation change instead; it does not
claim retry coverage.

Each section prints its actual `check()` passed/total count; the final summary
prints the exact total passed/failed checks and each failure. Final provider
summaries separately print observed request counts and internal-header leaks
for Codex, OpenAI compatibility, Claude, and Gemini. Counts come from the real
run, not hard-coded expected report totals. The image tag `latest` is unpinned,
so host configuration/transport changes may require fixture updates.
