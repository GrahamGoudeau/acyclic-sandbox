#!/usr/bin/env bash
# Vendor acyclic-sdk-js into web/sdk so the playground runs the real SDK.
# Usage: scripts/sync-sdk.sh [path to acyclic-sdk-js checkout]
set -euo pipefail
cd "$(dirname "$0")/.."
src="${1:-../js}"
rm -rf web/sdk && mkdir -p web/sdk
cp "$src"/src/*.js web/sdk/
(cd "$src" && git rev-parse --short HEAD 2>/dev/null || echo unversioned) > web/sdk/VERSION
echo "vendored $(ls web/sdk/*.js | wc -l) files from $src ($(cat web/sdk/VERSION))"
