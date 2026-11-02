#!/usr/bin/env bash
# Real CPA + Plus E2E with local Codex, OpenAI-compatible and Claude upstreams.
set -euo pipefail
cd "$(dirname "$0")"
project=cpa-key-quota-codex-e2e
compose=(docker compose -p "$project" -f docker-compose.yml)
E2E_TIMEOUT="${E2E_TIMEOUT:-900}"
run_compose() { timeout "$E2E_TIMEOUT" "${compose[@]}" "$@"; }
# Inspecting context metadata is local; permit only Unix sockets or loopback TCP.
if [[ -n "${DOCKER_CONTEXT:-}" ]]; then
  endpoint=$(docker context inspect "$DOCKER_CONTEXT" --format '{{.Endpoints.docker.Host}}')
elif [[ -n "${DOCKER_HOST:-}" ]]; then
  endpoint=$DOCKER_HOST
else
  endpoint=$(docker context inspect --format '{{.Endpoints.docker.Host}}')
fi
case "$endpoint" in
  unix://*|tcp://127.0.0.1:*|tcp://localhost:*|tcp://\[::1\]:*) ;;
  *) printf 'Refusing non-local Docker endpoint: %s\n' "$endpoint" >&2; exit 2 ;;
esac
cid=""
cleanup() {
  status=$?
  trap - EXIT
  if [[ -n "$cid" ]]; then
    docker rm -f "$cid" >/dev/null 2>&1 || true
  fi
  if [[ "${KEEP_STACK:-0}" != 1 ]]; then
    timeout 60 "${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
    rm -rf .run
  else
    echo "Stack retained (exit=$status); local UI: http://127.0.0.1:18517/v0/resource/plugins/cpa-key-quota/index.html"
    echo "Explicit cleanup from repo root: docker compose -p $project -f e2e/codex/docker-compose.yml down -v --remove-orphans"
  fi
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# The explicit project scopes both initial teardown and the EXIT cleanup.
run_compose down -v --remove-orphans >/dev/null 2>&1 || true
rm -rf .run && mkdir -p .run/certs .run/auths
# Throwaway CA + chatgpt.com leaf, trusted only by this CPA container.
openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj "/CN=cpa-e2e-ca" \
  -keyout .run/certs/ca.key -out .run/certs/ca.pem >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes -subj "/CN=chatgpt.com" \
  -keyout .run/certs/server.key -out .run/certs/server.csr >/dev/null 2>&1
printf 'subjectAltName=DNS:chatgpt.com,DNS:auth.openai.com,DNS:api.anthropic.com\nextendedKeyUsage=serverAuth\n' > .run/certs/ext.cnf
openssl x509 -req -in .run/certs/server.csr -CA .run/certs/ca.pem -CAkey .run/certs/ca.key \
  -CAcreateserial -days 2 -extfile .run/certs/ext.cnf -out .run/certs/server.pem >/dev/null 2>&1
chmod 644 .run/certs/*
cp cpa-config.yaml .run/config.yaml
cp auths/*.json .run/auths/

run_compose build
run_compose run --rm -T --no-deps builder
run_compose run --rm -T --no-deps fixtures
run_compose up -d mock plus
run_compose up -d cpa
code=0
# Detached execution preserves all output while retaining the test's own exit status.
if cid=$(run_compose run -d --no-deps e2e); then
  if code=$(timeout "$E2E_TIMEOUT" docker wait "$cid"); then
    if [[ ! "$code" =~ ^[0-9]+$ ]]; then code=125; fi
  else
    code=$?
  fi
  docker logs "$cid" 2>&1 || true
  docker rm -f "$cid" >/dev/null 2>&1 || true
  cid=""
else
  code=$?
fi
echo "======== cpa logs ========"
"${compose[@]}" logs --no-color --tail 80 cpa || true
echo "======== mock logs ========"
"${compose[@]}" logs --no-color --tail 30 mock || true
exit "$code"
