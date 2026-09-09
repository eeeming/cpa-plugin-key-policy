#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
compose=(docker compose -f docker-compose.yml)
# Bound every compose command so a hung container cannot hang CI forever.
E2E_TIMEOUT="${E2E_TIMEOUT:-900}"
if command -v timeout >/dev/null 2>&1; then
  run_with_timeout() { timeout "$E2E_TIMEOUT" "$@"; }
elif command -v gtimeout >/dev/null 2>&1; then
  run_with_timeout() { gtimeout "$E2E_TIMEOUT" "$@"; }
else
  run_with_timeout() { "$@"; }
fi
run_compose() { run_with_timeout "${compose[@]}" "$@"; }
# Always tear the stack down, including on an early failure (build/up/init).
cleanup() { "${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true; }
trap cleanup EXIT

run_compose down -v --remove-orphans >/dev/null 2>&1 || true
run_compose build
run_compose run --rm --no-deps builder
run_compose up -d home mock
run_compose run --rm --no-deps home-init
run_compose up -d cpa
set +e
run_compose run --rm --no-deps e2e
code=$?
set -e
echo "======== cpa logs ========"
"${compose[@]}" logs --no-color cpa | tail -n 120 || true
echo "======== home logs ========"
"${compose[@]}" logs --no-color home | tail -n 80 || true
echo "======== mock logs ========"
"${compose[@]}" logs --no-color mock | tail -n 40 || true
exit "$code"
