#!/bin/bash
# Build only. No installation, Launch Services registration, permissions,
# notifications, clipboard access, or network calls.
set -euo pipefail
if [[ "$(uname -s)" != Darwin ]]; then
  echo 'The notification helper must be built on macOS.' >&2
  exit 1
fi
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT="${1:-$ROOT}"
mkdir -p "$OUT"
OUT="$(cd "$OUT" && pwd)"
APP="$OUT/MoshdropNotifier.app"
if [[ -L "$APP" || ( -e "$APP" && ! -f "$APP/Contents/Resources/.moshdrop-generated" ) ]]; then
  echo "Refusing to overwrite an unrecognized app bundle: $APP" >&2
  exit 1
fi
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources"
# Independent of the Go release version: no false semantic-version claim about
# the helper. Updating an ad-hoc signature may require renewed alert permission.
printf 'moshdrop notifier build output\n' > "$APP/Contents/Resources/.moshdrop-generated"
cat > "$APP/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
  <key>CFBundleIdentifier</key><string>io.github.rrrrrrrrrricoxi.moshdrop.notifier</string>
  <key>CFBundleName</key><string>moshdrop</string>
  <key>CFBundleDisplayName</key><string>moshdrop</string>
  <key>CFBundleExecutable</key><string>MoshdropNotifier</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleShortVersionString</key><string>1.0.0</string>
  <key>CFBundleVersion</key><string>1</string>
  <key>LSUIElement</key><true/>
  <key>LSMinimumSystemVersion</key><string>13.0</string>
  <key>NSPrincipalClass</key><string>NSApplication</string>
</dict></plist>
PLIST
WORK="$(mktemp -d "${TMPDIR:-/tmp}/moshdrop-notifier-build.XXXXXXXX")"
trap 'rm -rf "$WORK"' EXIT
SDK="$(xcrun --sdk macosx --show-sdk-path)"
for ARCH in arm64 x86_64; do
  xcrun swiftc -parse-as-library -O -sdk "$SDK" -target "$ARCH-apple-macosx13.0" \
    -framework AppKit -framework UserNotifications \
    "$ROOT/native-notifier/Notifier.swift" -o "$WORK/$ARCH"
done
xcrun lipo -create "$WORK/arm64" "$WORK/x86_64" -output "$APP/Contents/MacOS/MoshdropNotifier"
chmod 755 "$APP/Contents/MacOS/MoshdropNotifier"
plutil -lint "$APP/Contents/Info.plist"
# Sign the bundle, not just the executable: bind its Info.plist and identifier.
# Ad-hoc signing is NOT Apple Developer ID signing or notarization.
codesign --force --sign - --identifier io.github.rrrrrrrrrricoxi.moshdrop.notifier "$APP"
codesign --verify --strict --verbose=2 "$APP"
printf '%s\n' "$APP"
