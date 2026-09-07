#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
compose=(docker compose -f docker-compose.yml)
"${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
set +e
"${compose[@]}" up --build --abort-on-container-exit --exit-code-from e2e
code=$?
set -e
echo "======== cpa logs ========"
"${compose[@]}" logs --no-color cpa | tail -n 120 || true
echo "======== mock logs ========"
"${compose[@]}" logs --no-color mock | tail -n 80 || true
"${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
exit "$code"
