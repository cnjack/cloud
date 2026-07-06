#!/usr/bin/env bash
# build.sh — cross-compile the three static binaries the Docker image needs, on
# the host, into ./bin/. Called by test.sh; also usable standalone.
#
# Env:
#   JCODE_SRC   path to the jcode source checkout (default: ../../jcode relative
#               to this script, i.e. /Users/jack/workpath/jjj/jcode)
#   TARGETARCH  amd64|arm64 (default: host arch; on Apple Silicon this is arm64,
#               which matches orb's native linux/arm64)
#   IMAGE       image tag to build (default: jcode-runner:local); set SKIP_DOCKER=1
#               to only build binaries.

set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
JCODE_SRC="${JCODE_SRC:-$(cd "$HERE/../../jcode" && pwd)}"
IMAGE="${IMAGE:-jcode-runner:local}"

# Resolve target arch (Go naming).
if [ -z "${TARGETARCH:-}" ]; then
  case "$(uname -m)" in
    arm64|aarch64) TARGETARCH=arm64 ;;
    x86_64|amd64)  TARGETARCH=amd64 ;;
    *) echo "unsupported host arch $(uname -m); set TARGETARCH" >&2; exit 1 ;;
  esac
fi

echo "[build] jcode source: $JCODE_SRC"
echo "[build] target: linux/$TARGETARCH"
[ -d "$JCODE_SRC/cmd/jcode" ] || { echo "[build] jcode source not found at $JCODE_SRC" >&2; exit 1; }

mkdir -p "$HERE/bin"

echo "[build] compiling jcode (headless, static)..."
( cd "$JCODE_SRC" && GOOS=linux GOARCH="$TARGETARCH" CGO_ENABLED=0 \
    go build -tags jcode_headless -ldflags "-s -w" -o "$HERE/bin/jcode" ./cmd/jcode )

echo "[build] compiling acpdrive..."
( cd "$HERE/acpdrive" && GOOS=linux GOARCH="$TARGETARCH" CGO_ENABLED=0 \
    go build -ldflags "-s -w" -o "$HERE/bin/acpdrive" . )

echo "[build] compiling orchclient..."
( cd "$HERE/orchclient" && GOOS=linux GOARCH="$TARGETARCH" CGO_ENABLED=0 \
    go build -ldflags "-s -w" -o "$HERE/bin/orchclient" . )

echo "[build] compiling mockllm..."
( cd "$HERE/mockllm" && GOOS=linux GOARCH="$TARGETARCH" CGO_ENABLED=0 \
    go build -ldflags "-s -w" -o "$HERE/bin/mockllm" . )

ls -la "$HERE/bin"

if [ "${SKIP_DOCKER:-0}" = "1" ]; then
  echo "[build] SKIP_DOCKER=1 — not building image"
  exit 0
fi

echo "[build] docker build -> $IMAGE (linux/$TARGETARCH)"
docker build --platform "linux/$TARGETARCH" -t "$IMAGE" "$HERE"
echo "[build] done: $IMAGE"
