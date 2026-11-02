#!/usr/bin/env bash
# Codex account binding E2E: real CPA host + Plus + mock chatgpt.com backend.
set -euo pipefail
cd "$(dirname "$0")"
compose=(docker compose -f docker-compose.yml)
E2E_TIMEOUT="${E2E_TIMEOUT:-900}"
run_compose() { timeout "$E2E_TIMEOUT" "${compose[@]}" "$@"; }
cleanup() {
  if [[ -z "${KEEP_STACK:-}" ]]; then
    "${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
    rm -rf .run
  fi
}
trap cleanup EXIT

"${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
rm -rf .run && mkdir -p .run/certs .run/auths
# Throwaway CA + chatgpt.com leaf, trusted only by this CPA container.
openssl req -x509 -newkey rsa:2048 -nodes -days 2 -subj "/CN=cpa-e2e-ca" \
  -keyout .run/certs/ca.key -out .run/certs/ca.pem >/dev/null 2>&1
openssl req -newkey rsa:2048 -nodes -subj "/CN=chatgpt.com" \
  -keyout .run/certs/server.key -out .run/certs/server.csr >/dev/null 2>&1
printf 'subjectAltName=DNS:chatgpt.com,DNS:auth.openai.com\nextendedKeyUsage=serverAuth\n' > .run/certs/ext.cnf
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
set +e
# Detached + docker logs: streaming attach drops output with a remote daemon.
cid=$("${compose[@]}" run -d --no-deps e2e)
code=$(timeout "$E2E_TIMEOUT" docker wait "$cid" || echo 124)
docker logs "$cid" 2>&1 | cat
docker rm -f "$cid" >/dev/null 2>&1
set -e
echo "======== cpa logs (plugin / scheduler) ========"
docker logs "$("${compose[@]}" ps -q cpa)" 2>&1 | grep -iE "plugin|cpa-key-quota|scheduler|error|warn" | tail -n 80 || true
echo "======== mock logs ========"
docker logs "$("${compose[@]}" ps -q mock)" 2>&1 | tail -n 30 || true
exit "$code"
