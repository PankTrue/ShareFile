#!/bin/bash
set -euo pipefail
APP_TARGET="$HOME/Applications/ShareFile.app"
if [ ! -d "$APP_TARGET" ]; then printf 'No ShareFile installation found in ~/Applications.\n'; exit 0; fi
IDENTIFIER="$(/usr/libexec/PlistBuddy -c 'Print CFBundleIdentifier' "$APP_TARGET/Contents/Info.plist")"
if [ "$IDENTIFIER" != app.sharefile.desktop ]; then printf 'Unexpected application identifier; stopping.\n'; exit 1; fi
printf 'Close ShareFile, then remove %s? [y/N] ' "$APP_TARGET"
read -r ANSWER
case "$ANSWER" in y|Y) rm -rf "$APP_TARGET" ;; *) exit 0 ;; esac
printf 'Application removed. Queue and Keychain credentials are retained.\n'
printf 'To revoke server access, remove this Mac in ShareFile Web UI → Account → Devices.\n'
