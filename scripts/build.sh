#!/usr/bin/env bash
# Baut statische Binaries für alle unterstützten NAS-Architekturen sowie das
# Mitschnitt-/Testwerkzeug für Desktop-Systeme. Benötigt nur Go (>= 1.24).
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION="${VERSION:-$(cat VERSION)}"
LDFLAGS="-s -w -X main.version=$VERSION"
export CGO_ENABLED=0

# NAS-Ziele: Verzeichnisname (wie scripts/common: arch_dir) → GOOS/GOARCH/GOARM
NAS_TARGETS=(
  "x86_64:linux:amd64:"
  "aarch64:linux:arm64:"
  "armv7:linux:arm:7"
  "armv5:linux:arm:5"
  "i686:linux:386:"
)
# Desktop-Werkzeug (capture/analyze/probe) für den eigenen Rechner
TOOL_TARGETS=(
  "macos-arm64:darwin:arm64:"
  "macos-x86_64:darwin:amd64:"
  "windows-x86_64.exe:windows:amd64:"
  "linux-x86_64:linux:amd64:"
)

rm -rf build/bin build/tools
for t in "${NAS_TARGETS[@]}"; do
  IFS=: read -r dir os arch arm <<<"$t"
  mkdir -p "build/bin/$dir"
  GOOS=$os GOARCH=$arch GOARM=$arm go build -trimpath -ldflags "$LDFLAGS" -o "build/bin/$dir/dsvr" ./cmd/dsvr
  echo "  NAS  $dir  $(du -h "build/bin/$dir/dsvr" | cut -f1)"
done
mkdir -p build/tools
for t in "${TOOL_TARGETS[@]}"; do
  IFS=: read -r name os arch arm <<<"$t"
  GOOS=$os GOARCH=$arch GOARM=$arm go build -trimpath -ldflags "$LDFLAGS" -o "build/tools/dsvr-$name" ./cmd/dsvr
  echo "  Tool $name"
done
