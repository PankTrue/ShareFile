#!/bin/sh
set -eu
# Called inside an isolated dbus-run-session / Xvfb with a disposable HOME.
mkdir -p "$XDG_DATA_HOME/keyrings"
printf '%s' 'ephemeral-test-keyring-password' | gnome-keyring-daemon --unlock --components=secrets >/dev/null
exec python3 -m unittest discover -s clients/linux/tests -p test_gtk.py -v
