#!/bin/sh
set -eu
arch=$(uname -m)
case "$arch" in
  aarch64|arm64) goarch=arm64 ;;
  x86_64|amd64) goarch=amd64 ;;
  *) goarch=$arch ;;
esac
mkdir -p "/out/linux/${goarch}"
cp /cpa-key-quota.so "/out/linux/${goarch}/cpa-key-quota.so"
cp /cpa-key-quota.so /out/cpa-key-quota.so
ls -la /out /out/linux/"${goarch}"
echo "installed cpa-key-quota.so for linux/${goarch}"
