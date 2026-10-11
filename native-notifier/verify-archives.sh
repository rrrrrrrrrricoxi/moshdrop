#!/bin/bash
# Validate build artifacts only. No installation, permission requests,
# notifications, clipboard operations, or SSH.
set -euo pipefail
shopt -s nullglob
ARCHIVES=(dist/moshdrop_*_darwin_*.tar.gz)
if [[ ${#ARCHIVES[@]} != 2 ]]; then
  echo "Expected exactly two Darwin release archives, found ${#ARCHIVES[@]}" >&2
  exit 1
fi
WORK="$(mktemp -d "${TMPDIR:-/tmp}/moshdrop-archive-check.XXXXXXXX")"
trap 'rm -rf "$WORK"' EXIT
HOST="$(uname -m)"
for ARCHIVE in "${ARCHIVES[@]}"; do
  case "$ARCHIVE" in
    *_arm64.tar.gz) ARCH=arm64 ;;
    *_amd64.tar.gz) ARCH=amd64 ;;
    *) echo "Unexpected archive: $ARCHIVE" >&2; exit 1 ;;
  esac
  DEST="$WORK/$ARCH"
  mkdir -p "$DEST"
  tar -xzf "$ARCHIVE" -C "$DEST"
  test -x "$DEST/moshdrop"
  APP="$DEST/MoshdropNotifier.app"
  test -x "$APP/Contents/MacOS/MoshdropNotifier"
  test -f "$APP/Contents/Resources/.moshdrop-generated"
  plutil -lint "$APP/Contents/Info.plist"
  codesign --verify --strict --verbose=2 "$APP"
  SLICES="$(xcrun lipo -archs "$APP/Contents/MacOS/MoshdropNotifier")"
  [[ "$SLICES" == *arm64* && "$SLICES" == *x86_64* ]]
  "$APP/Contents/MacOS/MoshdropNotifier" --selftest
  go version -m "$DEST/moshdrop" | grep -F "GOARCH=$ARCH"
  # Never require Rosetta or execute a foreign-architecture Go binary.
  if [[ "$HOST" == arm64 && "$ARCH" == arm64 || "$HOST" == x86_64 && "$ARCH" == amd64 ]]; then
    MOSHDROP_STATE_DIR="$DEST/empty-state" "$DEST/moshdrop" --version
  fi
  printf 'PASS archive layout/signature/architecture: %s\n' "$ARCHIVE"
done
FORMULAS=()
while IFS= read -r FILE; do FORMULAS+=("$FILE"); done < <(find dist -type f -name moshdrop.rb)
if [[ ${#FORMULAS[@]} == 0 ]]; then
  echo 'Expected a generated Homebrew formula in dist' >&2
  exit 1
fi
for FORMULA in "${FORMULAS[@]}"; do
  ruby -c "$FORMULA"
  grep -F 'libexec.install "MoshdropNotifier.app"' "$FORMULA"
  printf 'PASS formula syntax/helper install: %s\n' "$FORMULA"
done
