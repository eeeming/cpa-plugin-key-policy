#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
compose=(docker compose -f docker-compose.yml)
"${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
"${compose[@]}" build
"${compose[@]}" run --rm --no-deps builder
"${compose[@]}" up -d plus mock
"${compose[@]}" run --rm --no-deps plus-init
"${compose[@]}" up -d cpa
set +e
"${compose[@]}" run --rm --no-deps e2e
code=$?
set -e
if [[ -n "${STATUS_FILE:-}" ]]; then
  curl -sS -H "Authorization: Bearer e2e-mgmt-key" \
    http://127.0.0.1:18417/v0/management/plugins/cpa-key-quota/status \
    | tee "${STATUS_FILE}" || true
fi
echo "======== cpa logs ========"
"${compose[@]}" logs --no-color cpa | tail -n 120 || true
echo "======== plus logs ========"
"${compose[@]}" logs --no-color plus | tail -n 80 || true
echo "======== mock logs ========"
"${compose[@]}" logs --no-color mock | tail -n 40 || true
# keep stack only on failure for debug; always dump status files from e2e container is gone
"${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
exit "$code"
