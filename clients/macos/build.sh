#!/bin/bash
set -euo pipefail
MAC_DIR="$(cd "$(dirname "$0")" && pwd)"
ROOT_DIR="$(cd "$MAC_DIR/../.." && pwd)"
BUILD_DIR="$MAC_DIR/build"
GO_BIN="${GO:-go}"
SIGN_IDENTITY="${SIGN_IDENTITY:--}"
mkdir -p "$BUILD_DIR"
STAGE_DIR="$(mktemp -d "${TMPDIR:-/tmp}/sharefile-macos-build.XXXXXX")"
trap 'rm -rf "$STAGE_DIR"' EXIT
APP_DIR="$STAGE_DIR/ShareFile.app"
mkdir -p "$APP_DIR/Contents/MacOS" "$APP_DIR/Contents/Resources"
cp "$MAC_DIR/Resources/Info.plist" "$APP_DIR/Contents/Info.plist"
for ARCH in arm64 x86_64; do
    GO_ARCH="$ARCH"
    if [ "$ARCH" = x86_64 ]; then GO_ARCH=amd64; fi
    (cd "$ROOT_DIR/clients/core" && GOOS=darwin GOARCH="$GO_ARCH" CGO_ENABLED=0 "$GO_BIN" build -trimpath -ldflags='-s -w' -o "$STAGE_DIR/core-$ARCH" ./cmd/sharefile-client)
    xcrun swiftc -swift-version 5 -O -target "$ARCH-apple-macosx13.0" \
        -module-cache-path "$BUILD_DIR/module-cache" \
        "$MAC_DIR"/Sources/*.swift -o "$STAGE_DIR/ui-$ARCH" \
        -framework AppKit -framework SwiftUI -framework Security -framework UserNotifications -framework Carbon
done
xcrun lipo -create "$STAGE_DIR/core-arm64" "$STAGE_DIR/core-x86_64" -output "$APP_DIR/Contents/MacOS/sharefile-client"
xcrun lipo -create "$STAGE_DIR/ui-arm64" "$STAGE_DIR/ui-x86_64" -output "$APP_DIR/Contents/MacOS/ShareFile"
xcrun swift -module-cache-path "$BUILD_DIR/module-cache" "$MAC_DIR/Resources/icon.swift" "$STAGE_DIR/AppIcon.iconset"
iconutil -c icns "$STAGE_DIR/AppIcon.iconset" -o "$APP_DIR/Contents/Resources/AppIcon.icns"
# Work on a clean copy so Finder/resource-fork metadata cannot contaminate signing.
ditto --norsrc --noextattr "$APP_DIR" "$STAGE_DIR/clean/ShareFile.app"
APP_DIR="$STAGE_DIR/clean/ShareFile.app"
for EXECUTABLE in sharefile-client ShareFile; do
    codesign --force --sign "$SIGN_IDENTITY" --options runtime "$APP_DIR/Contents/MacOS/$EXECUTABLE"
done
codesign --force --sign "$SIGN_IDENTITY" --options runtime "$APP_DIR"
codesign --verify --deep --strict "$APP_DIR"
if [ -e "$BUILD_DIR/ShareFile.app" ]; then rm -rf "$BUILD_DIR/ShareFile.app"; fi
ditto --norsrc --noextattr "$APP_DIR" "$BUILD_DIR/ShareFile.app"
cp "$MAC_DIR/Install.command" "$BUILD_DIR/Install.command"
cp "$MAC_DIR/Uninstall.command" "$BUILD_DIR/Uninstall.command"
chmod +x "$BUILD_DIR/Install.command" "$BUILD_DIR/Uninstall.command"
mkdir -p "$STAGE_DIR/package"
ditto --norsrc --noextattr "$APP_DIR" "$STAGE_DIR/package/ShareFile.app"
cp "$BUILD_DIR/Install.command" "$BUILD_DIR/Uninstall.command" "$STAGE_DIR/package/"
cp "$ROOT_DIR/docs/macos.md" "$STAGE_DIR/package/README.md"
ditto -c -k --norsrc --noextattr --keepParent "$STAGE_DIR/package" "$BUILD_DIR/ShareFile-macOS-universal.zip"
printf 'Built: %s\n' "$BUILD_DIR/ShareFile.app" "$BUILD_DIR/ShareFile-macOS-universal.zip"
