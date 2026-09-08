#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"
compose=(docker compose -f docker-compose.yml)
"${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
"${compose[@]}" build
"${compose[@]}" run --rm --no-deps builder
"${compose[@]}" up -d home mock
"${compose[@]}" run --rm --no-deps home-init
"${compose[@]}" up -d cpa
set +e
"${compose[@]}" run --rm --no-deps e2e
code=$?
set -e
echo "======== cpa logs ========"
"${compose[@]}" logs --no-color cpa | tail -n 120 || true
echo "======== home logs ========"
"${compose[@]}" logs --no-color home | tail -n 80 || true
echo "======== mock logs ========"
"${compose[@]}" logs --no-color mock | tail -n 40 || true
"${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
exit "$code"
