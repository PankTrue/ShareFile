#!/bin/bash
set -euo pipefail
PACKAGE_DIR="$(cd "$(dirname "$0")" && pwd)"
APP_SOURCE="$PACKAGE_DIR/ShareFile.app"
APP_TARGET="$HOME/Applications/ShareFile.app"
if [ ! -d "$APP_SOURCE" ]; then printf 'ShareFile.app must be beside this installer.\n'; exit 1; fi
codesign --verify --deep --strict "$APP_SOURCE"
mkdir -p "$HOME/Applications"
if [ -e "$APP_TARGET" ]; then
    printf 'Close ShareFile before updating. Replace %s? [y/N] ' "$APP_TARGET"
    read -r ANSWER
    case "$ANSWER" in y|Y) ;; *) exit 0 ;; esac
    BACKUP_DIR="$(mktemp -d "$HOME/Applications/ShareFile-previous.XXXXXX")"
    mv "$APP_TARGET" "$BACKUP_DIR/ShareFile.app"
    printf 'Previous version saved: %s\n' "$BACKUP_DIR"
fi
ditto --norsrc --noextattr "$APP_SOURCE" "$APP_TARGET"
open "$APP_TARGET"
printf 'Installed. Finder → Services → ShareFile: Загрузить и поделиться.\n'
printf 'If the item is hidden, enable it in Keyboard → Keyboard Shortcuts → Services.\n'
