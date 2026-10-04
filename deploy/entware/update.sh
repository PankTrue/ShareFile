#!/bin/sh
# Replace application files only; keep config, TLS and storage intact.
set -eu
umask 077
PATH=/opt/sbin:/opt/bin:/usr/sbin:/usr/bin:/sbin:/bin
export PATH
release_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
web=/opt/share/sharefile/web
init=/opt/etc/init.d/S90sharefile
service=/opt/etc/sharefile-service
fail() { echo "$*" >&2; exit 1; }
for path in /opt/etc/sharefile.json /opt/sbin/sharefile "$init" "$service/run"; do
    test -f "$path" || fail "Incomplete existing installation: missing $path."
done
test -d "$web" && test ! -L "$web" || fail 'Expected a regular Web UI directory at /opt/share/sharefile/web.'
for path in sharefile-linux-arm64 S90sharefile service/run dist/index.html; do
    test -f "$release_dir/$path" || fail "Incomplete release: missing $path."
done
chmod 755 "$release_dir/sharefile-linux-arm64"
"$release_dir/sharefile-linux-arm64" serve -help >/dev/null 2>&1 || fail 'The downloaded binary cannot run on this router.'
opkg update
opkg install curl jq
jq -e '.web_dir == "/opt/share/sharefile/web"' /opt/etc/sharefile.json >/dev/null || fail 'Automatic update requires the standard web_dir; update a custom layout manually.'
health_url="http://$(jq -er '.public_addr' /opt/etc/sharefile.json)/readyz"

mkdir -p /opt/var/backups/sharefile
backup=$(mktemp -d /opt/var/backups/sharefile/release.XXXXXX)
cp /opt/sbin/sharefile "$backup/sharefile"
cp /opt/etc/sharefile.json "$backup/sharefile.json"
cp "$init" "$backup/S90sharefile"
cp "$service/run" "$backup/run"
next_binary=$(mktemp /opt/sbin/.sharefile-next.XXXXXX)
next_web=$(mktemp -d /opt/share/sharefile/.web-next.XXXXXX)
changed=0
complete=0
finish() {
    if test "$changed" -eq 1 && test "$complete" -eq 0; then
        echo "Update failed; restoring previous application files from $backup." >&2
        "$init" stop || true
        cp "$backup/sharefile" "$next_binary"
        chmod 755 "$next_binary"
        mv "$next_binary" /opt/sbin/sharefile
        if test -d "$backup/web"; then
            if test -e "$web"; then mv "$web" "$backup/failed-web"; fi
            mv "$backup/web" "$web"
        fi
        cp "$backup/run" "$service/run"
        cp "$backup/S90sharefile" "$init"
        chmod 755 "$service/run" "$init"
        "$init" start || true
    fi
    rm -f "$next_binary"
    rm -rf "$next_web"
}
trap finish 0
cp "$release_dir/sharefile-linux-arm64" "$next_binary"
chmod 755 "$next_binary"
cp -R "$release_dir/dist/." "$next_web/"
"$init" stop
changed=1
mv "$web" "$backup/web"
mv "$next_web" "$web"
mv "$next_binary" /opt/sbin/sharefile
cp "$release_dir/service/run" "$service/run"
cp "$release_dir/S90sharefile" "$init"
chmod 755 "$service/run" "$init"
"$init" start
count=0
until curl --fail --silent --max-time 2 "$health_url" >/dev/null; do
    count=$((count + 1))
    test "$count" -lt 20 || fail 'Updated server did not become ready.'
    sleep 1
done
"$init" check
complete=1
echo "Updated. Previous application files: $backup"
echo 'Configuration, certificates, accounts and stored files were preserved.'
