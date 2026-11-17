#!/usr/bin/env bash
# Statische Prüfung, Unit-/Integrationstests (inkl. Race-Detector) und ein
# End-to-End-Lauf: Server (lokaler Modus) + Client-Probe mit Testvideo.
set -euo pipefail
cd "$(dirname "$0")/.."
go vet ./...
go test -race -count=1 ./...

T=$(mktemp -d); trap 'kill $PID 2>/dev/null || true; rm -rf "$T"' EXIT
mkdir -p "$T/media/Testfilm (2024)" "$T/var"
if command -v ffmpeg >/dev/null; then
  ffmpeg -loglevel error -f lavfi -i testsrc=size=320x240:rate=25 -t 2 -c:v libx264 -pix_fmt yuv420p -movflags +faststart "$T/media/Testfilm (2024)/Testfilm (2024).mp4"
else
  printf '\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2' > "$T/media/Testfilm (2024)/Testfilm (2024).mp4"
fi
go build -o "$T/dsvr" ./cmd/dsvr
DSVR_PASSWORD=e2e-pass "$T/dsvr" adduser --users "$T/var/users.json" --account tester --manager >/dev/null
"$T/dsvr" init-config --config "$T/var/config.json" --auth local --users "$T/var/users.json" --port 5199 --folders "movie:$T/media" >/dev/null
"$T/dsvr" serve --config "$T/var/config.json" > "$T/var/log" 2>&1 & PID=$!
for i in $(seq 1 30); do curl -sf http://127.0.0.1:5199/dsvr/api/health >/dev/null && break; sleep 0.2; done
DSVR_PASSWORD=e2e-pass "$T/dsvr" probe --base http://127.0.0.1:5199 --account tester
echo "E2E OK"
