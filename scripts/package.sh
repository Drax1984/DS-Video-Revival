#!/usr/bin/env bash
# Erzeugt je CPU-Architektur ein Paket dist/dsvideorevival-<version>-<arch>.spk
# (x86_64, aarch64, armv7 – siehe README) und prüft den Paketaufbau.
# ALL=1 baut zusätzlich das Gesamtpaket sowie armv5/i686 (nicht veröffentlicht).
set -euo pipefail
cd "$(dirname "$0")/.."
VERSION="${VERSION:-$(cat VERSION)}"
BUILD="${BUILD:-0001}"
PKGVER="$VERSION-$BUILD"
PORT=5180
SRC=packaging/synology
STAGE=build/spk
OUT="dist/dsvideorevival-$PKGVER.spk"

# Immer neu bauen, damit nie veraltete Binaries ins Paket geraten.
rm -rf build/bin && VERSION="$VERSION" scripts/build.sh
command -v jq >/dev/null || { echo "jq fehlt"; exit 1; }
tar --version 2>/dev/null | grep -q "GNU tar" || { echo "GNU tar fehlt"; exit 1; }

# build_spk <ausgabe> <arch…>: ein Paket mit den Binaries der genannten Architekturen.
build_spk() {
  local out="$1"; shift
  rm -rf "$STAGE"; mkdir -p "$STAGE/payload/bin" dist
  for a in "$@"; do cp -R "build/bin/$a" "$STAGE/payload/bin/"; done
  chmod 755 "$STAGE"/payload/bin/*/dsvr
  cp "$SRC/dsvideorevival.sc" "$STAGE/payload/"
  # nginx-Einbindung in DSMs Webserver (Ressourcen-Worker web-config, Typ dsm)
  mkdir -p "$STAGE/payload/nginx"
  sed "s/@PORT@/$PORT/g" "$SRC/nginx/dsm.conf" > "$STAGE/payload/nginx/dsm.conf"
  if command -v nginx >/dev/null; then
    t=$(mktemp -d); printf 'pid %s/p; error_log stderr; events{} http{ server { listen 127.0.0.1:1; include %s; } }\n' "$t" "$PWD/$STAGE/payload/nginx/dsm.conf" > "$t/n.conf"
    nginx -q -t -c "$t/n.conf" -p "$t" || { echo "nginx-Konfiguration ungültig"; exit 1; }
    rm -rf "$t"
  fi
  mkdir -p "$STAGE/payload/ui"
  cp "$SRC/ui/config" "$STAGE/payload/ui/config"
  go run ./tools/genicons "$STAGE/gen"
  cp -R "$STAGE/gen/ui/images" "$STAGE/payload/ui/"
  cp LICENSE NOTICE.md "$STAGE/payload/"

  cp -R "$SRC/conf" "$SRC/scripts" "$SRC/WIZARD_UIFILES" "$STAGE/"
  cp "$STAGE/gen/PACKAGE_ICON.PNG" "$STAGE/gen/PACKAGE_ICON_256.PNG" "$STAGE/"
  for jf in conf/privilege conf/resource WIZARD_UIFILES/install_uifile; do
    jq empty "$STAGE/$jf" || { echo "Ungültiges JSON: $jf"; exit 1; }
  done
  jq empty "$STAGE/payload/ui/config"
  for s in "$STAGE"/scripts/*; do sh -n "$s"; done

  TAR=(tar --owner=0 --group=0 --numeric-owner --no-xattrs --sort=name --mtime="@${SOURCE_DATE_EPOCH:-$(git log -1 --format=%ct 2>/dev/null || date +%s)}")
  "${TAR[@]}" -czf "$STAGE/package.tgz" -C "$STAGE/payload" .
  sed -e "s/@VERSION@/$PKGVER/" -e "s/@PORT@/$PORT/" "$SRC/INFO.in" > "$STAGE/INFO"
  echo "checksum=\"$(md5sum "$STAGE/package.tgz" | cut -d' ' -f1)\"" >> "$STAGE/INFO"
  echo "extractsize=\"$(( $(du -sk "$STAGE/payload" | cut -f1) ))\"" >> "$STAGE/INFO"

  "${TAR[@]}" -cf "$out" -C "$STAGE" INFO package.tgz conf scripts WIZARD_UIFILES PACKAGE_ICON.PNG PACKAGE_ICON_256.PNG
  ( cd dist && sha256sum "$(basename "$out")" > "$(basename "$out").sha256" )
  echo "Gebaut: $out ($(du -h "$out" | cut -f1))"
}

# Je CPU ein kleines Paket (< 8 MB); veröffentlicht werden x86_64, aarch64, armv7.
ARCHS="x86_64 aarch64 armv7"
if [ "${ALL:-0}" = 1 ]; then
  ARCHS=$(cd build/bin && ls)
  build_spk "$OUT" $ARCHS
fi
for a in $ARCHS; do
  build_spk "dist/dsvideorevival-$PKGVER-$a.spk" "$a"
done
