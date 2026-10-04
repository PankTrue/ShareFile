#!/bin/sh
set -eu
umask 077
PATH=/opt/sbin:/opt/bin:/usr/sbin:/usr/bin:/sbin:/bin
export PATH

release_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
storage_mount=/tmp/mnt/745ee3e5-6027-4b29-8e14-3fdc29729706

fail() {
    echo "$*" >&2
    exit 1
}

check_mounts() {
    awk -v target="$storage_mount" '
        $2 == target && $3 == "ext4" && $4 ~ /^rw(,|$)/ { found = 1 }
        END { exit !found }
    ' /proc/mounts || fail "Storage disk is not mounted read-write as ext4 at $storage_mount."
    awk '
        $2 == "/opt" && $4 ~ /^rw(,|$)/ && $4 !~ /(^|,)noexec(,|$)/ { found = 1 }
        END { exit !found }
    ' /proc/mounts || fail "Entware /opt must be mounted read-write and allow execution."
}

test "$(uname -m)" = aarch64 || fail "This release requires Linux aarch64."
check_mounts

for path in /opt/etc/sharefile.json /opt/sbin/sharefile /opt/etc/sharefile-service /opt/etc/init.d/S90sharefile /opt/share/sharefile/web "$storage_mount/sharefile"; do
    if test -e "$path" || test -L "$path"; then
        fail "First installation only: $path already exists. Nothing has been changed."
    fi
done
for path in sharefile-linux-arm64 sharefile.lan.example.json tls.sh S90sharefile service/run dist/index.html; do
    test -f "$release_dir/$path" || fail "Incomplete release: missing $path."
done
test -t 0 || fail "Interactive terminal required for the administrator password; connect with ssh -t."
command -v opkg >/dev/null || fail "Run this script in the Entware shell."
df -h "$storage_mount"

opkg update
opkg install daemonize daemontools-encore openssl-util
mkdir -p /opt/sbin /opt/etc/init.d /opt/etc/sharefile-service /opt/var/log/sharefile /opt/share/sharefile/web
chmod 700 /opt/etc/sharefile-service /opt/var/log/sharefile
cp "$release_dir/sharefile-linux-arm64" /opt/sbin/sharefile
cp "$release_dir/sharefile.lan.example.json" /opt/etc/sharefile.json
cp "$release_dir/service/run" /opt/etc/sharefile-service/run
cp -R "$release_dir/dist/." /opt/share/sharefile/web/
chmod 755 /opt/sbin/sharefile /opt/etc/sharefile-service/run
chmod 600 /opt/etc/sharefile.json
. "$release_dir/tls.sh"
sharefile_prepare_tls /opt/etc/sharefile 192.168.1.1

# Register boot startup only after storage and administrator creation succeed.
check_mounts
/opt/sbin/sharefile init-storage -config /opt/etc/sharefile.json
/opt/sbin/sharefile admin-create -config /opt/etc/sharefile.json -username admin
cp "$release_dir/S90sharefile" /opt/etc/init.d/S90sharefile
chmod 755 /opt/etc/init.d/S90sharefile
/opt/etc/init.d/S90sharefile start
/opt/etc/init.d/S90sharefile check
echo 'Installed. Trust /opt/etc/sharefile/lan-ca.pem on your computer, then open https://192.168.1.1:8081'
openssl x509 -in /opt/etc/sharefile/lan-ca.pem -noout -fingerprint -sha256
