#!/usr/bin/env bash
# Build pi-bridge.so for the CLIProxyAPI container (linux/amd64).
#
# The plugin must be compiled against the same SDK version as the running
# CLIProxyAPI image, and cgo requires a matching toolchain, so the build runs
# inside a Go container rather than on the host.
#
# The artifact is named pi-bridge-v<VERSION>.so because the host only
# hot-reloads a plugin when its file PATH changes; overwriting a .so in place
# would require a container restart.
set -euo pipefail

CPA_SDK_VERSION="${CPA_SDK_VERSION:-v7.2.93}"
GO_IMAGE="${GO_IMAGE:-golang:1.26}"
OUT_DIR="${OUT_DIR:-dist}"

cd "$(dirname "$0")"
mkdir -p "$OUT_DIR"

# Single source of truth: the version constant compiled into the plugin.
VERSION="$(sed -n 's/.*pluginVersion = "\(.*\)".*/\1/p' main.go)"
if [ -z "$VERSION" ]; then
  echo "could not determine pluginVersion from main.go" >&2
  exit 1
fi
ARTIFACT="pi-bridge-v${VERSION}.so"

echo "Building ${ARTIFACT} (SDK ${CPA_SDK_VERSION}, linux/amd64)"

docker run --rm \
  --platform linux/amd64 \
  -v "$PWD":/src \
  -w /src \
  -e GOFLAGS="${GOFLAGS:--mod=mod}" \
  -e CGO_ENABLED=1 \
  "$GO_IMAGE" \
  go build -buildmode=c-shared -trimpath -o "$OUT_DIR/$ARTIFACT" .

echo "Built: $OUT_DIR/$ARTIFACT"
ls -la "$OUT_DIR/$ARTIFACT"
