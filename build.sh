#!/usr/bin/env bash
# Build pi-bridge.so for the CLIProxyAPI container (linux/amd64).
#
# The plugin must be compiled against the same SDK version as the running
# CLIProxyAPI image, and cgo requires a matching toolchain, so the build runs
# inside a Go container rather than on the host.
set -euo pipefail

CPA_SDK_VERSION="${CPA_SDK_VERSION:-v7.2.93}"
GO_IMAGE="${GO_IMAGE:-golang:1.26}"
OUT_DIR="${OUT_DIR:-dist}"

cd "$(dirname "$0")"
mkdir -p "$OUT_DIR"

echo "Building pi-bridge.so (SDK ${CPA_SDK_VERSION}, linux/amd64)"

docker run --rm \
  --platform linux/amd64 \
  -v "$PWD":/src \
  -w /src \
  -e GOFLAGS=-mod=mod \
  -e CGO_ENABLED=1 \
  "$GO_IMAGE" \
  go build -buildmode=c-shared -trimpath -o "$OUT_DIR/pi-bridge.so" .

echo "Built: $OUT_DIR/pi-bridge.so"
ls -la "$OUT_DIR/pi-bridge.so"
