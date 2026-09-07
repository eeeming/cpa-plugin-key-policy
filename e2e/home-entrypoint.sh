#!/bin/sh
set -eu
mkdir -p /CLIProxyAPIHome/data /CLIProxyAPIHome/auths
if [ ! -f /CLIProxyAPIHome/data/.imported ]; then
  ./CLIProxyAPIHome -import \
    -config /import/config.yaml \
    -auth-dir /CLIProxyAPIHome/auths \
    -sqlite-path /CLIProxyAPIHome/data/home.db
  touch /CLIProxyAPIHome/data/.imported
fi
exec ./CLIProxyAPIHome -addr 0.0.0.0:8327 -sqlite-path /CLIProxyAPIHome/data/home.db
