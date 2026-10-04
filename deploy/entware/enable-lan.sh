#!/bin/sh
set -eu
umask 077
PATH=/opt/sbin:/opt/bin:/usr/sbin:/usr/bin:/sbin:/bin
export PATH

release_dir=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
config=/opt/etc/sharefile.json
test -f "$config" && test -x /opt/sbin/sharefile || {
    echo 'ShareFile is not installed; run first-install.sh first.' >&2
    exit 1
}
for path in tls.sh S90sharefile service/run; do
    test -f "$release_dir/$path" || { echo "Incomplete release: missing $path." >&2; exit 1; }
done

opkg update
opkg install openssl-util jq daemonize daemontools-encore
. "$release_dir/tls.sh"
sharefile_prepare_tls /opt/etc/sharefile 192.168.1.1

new_config=$(mktemp /opt/etc/.sharefile-lan.XXXXXX)
trap 'rm -f "$new_config"' 0
# Preserve the storage identity, accounts, transfer settings and import paths.
jq '
    .public_addr = "127.0.0.1:8080" |
    .public_url = "https://192.168.1.1:8081" |
    .admin_addr = "192.168.1.1:8081" |
    .admin_url = "https://192.168.1.1:8081" |
    .admin_cert = "/opt/etc/sharefile/admin-cert.pem" |
    .admin_key = "/opt/etc/sharefile/admin-key.pem" |
    .insecure_local = false
' "$config" > "$new_config"
backup=$(mktemp /opt/etc/sharefile.json.before-lan.XXXXXX)
cp "$config" "$backup"
chmod 600 "$backup" "$new_config"

mkdir -p /opt/etc/init.d /opt/etc/sharefile-service
cp "$release_dir/S90sharefile" /opt/etc/init.d/S90sharefile
chmod 755 /opt/etc/init.d/S90sharefile
/opt/etc/init.d/S90sharefile stop
cp "$release_dir/service/run" /opt/etc/sharefile-service/run
chmod 755 /opt/etc/sharefile-service/run
mv "$new_config" "$config"
/opt/etc/init.d/S90sharefile start
/opt/etc/init.d/S90sharefile check
echo "Previous configuration: $backup"
echo 'LAN/VPN access: https://192.168.1.1:8081'
echo 'Trust /opt/etc/sharefile/lan-ca.pem on each client. CA fingerprint:'
openssl x509 -in /opt/etc/sharefile/lan-ca.pem -noout -fingerprint -sha256
