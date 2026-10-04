#!/bin/sh
# Download and install/update an official GitHub release in the Entware shell.
set -eu
umask 077
PATH=/opt/sbin:/opt/bin:/usr/sbin:/usr/bin:/sbin:/bin
export PATH

repository=${SHAREFILE_REPOSITORY:-PankTrue/ShareFile}
version=latest
fail() { echo "$*" >&2; exit 1; }
case "${1:-}" in
    '') ;;
    --version) test "$#" -eq 2 || fail 'Usage: sh install-sharefile.sh [--version vX.Y.Z]'; version=$2 ;;
    -h|--help) echo 'Usage: sh install-sharefile.sh [--version vX.Y.Z]'; exit 0 ;;
    *) fail 'Usage: sh install-sharefile.sh [--version vX.Y.Z]' ;;
esac
test "$(id -u)" -eq 0 || fail 'Run this installer as root in the Entware shell.'
test "$(uname -m)" = aarch64 || fail 'This release requires Linux aarch64.'
case "$repository" in *[!A-Za-z0-9_./-]*|/*|*/../*|*..*) fail 'Invalid SHAREFILE_REPOSITORY.' ;; esac
test "$(printf '%s' "$repository" | tr -cd / | wc -c)" -eq 1 || fail 'Repository must be owner/name.'
for command in curl sha256sum tar awk; do
    command -v "$command" >/dev/null || fail "Missing $command; install curl, ca-bundle, coreutils-sha256sum and any missing shell tools with opkg first."
done
awk '$2 == "/opt" && $4 ~ /^rw(,|$)/ && $4 !~ /(^|,)noexec(,|$)/ { found=1 } END { exit !found }' /proc/mounts || fail 'Entware /opt must be mounted read-write and allow execution.'

download() {
    curl --fail --show-error --location --proto '=https' --proto-redir '=https' \
        --connect-timeout 15 --retry 3 "$@"
}
if test "$version" = latest; then
    resolved=$(download --head --silent --output /dev/null --write-out '%{url_effective}' "https://github.com/$repository/releases/latest")
    case "$resolved" in "https://github.com/$repository/releases/tag/"*) version=${resolved##*/} ;; *) fail 'No published release found. Publish a v* tag first.' ;; esac
fi
case "$version" in v[0-9]*) ;; *) fail 'Release tags must start with v and a digit.' ;; esac
case "$version" in *[!A-Za-z0-9._-]*) fail 'Invalid release tag.' ;; esac

mkdir -p /opt/tmp
work=$(mktemp -d /opt/tmp/sharefile-download.XXXXXX)
trap 'rm -rf "$work"' 0
base="https://github.com/$repository/releases/download/$version"
echo "Downloading ShareFile $version..."
download --output "$work/sharefile-router.tar.gz" "$base/sharefile-router.tar.gz"
download --output "$work/sharefile-router.tar.gz.sha256" "$base/sharefile-router.tar.gz.sha256"
awk 'NF != 2 || length($1) != 64 || $1 ~ /[^0-9a-f]/ || $2 != "sharefile-router.tar.gz" { bad=1 } END { exit bad || NR != 1 }' "$work/sharefile-router.tar.gz.sha256" || fail 'Invalid release checksum file.'
(cd "$work" && sha256sum -c sharefile-router.tar.gz.sha256) || fail 'Release checksum mismatch; nothing has been installed.'
tar -tzf "$work/sharefile-router.tar.gz" > "$work/entries"
while IFS= read -r entry; do
    case "$entry" in /*|..|../*|*/../*|*/..) fail 'Unsafe release archive path.' ;; esac
done < "$work/entries"
tar -tvzf "$work/sharefile-router.tar.gz" | awk 'substr($0,1,1) != "-" && substr($0,1,1) != "d" { bad=1 } END { exit bad }' || fail 'Release archive must not contain links or special files.'
mkdir "$work/release"
tar -xzf "$work/sharefile-router.tar.gz" -C "$work/release"
if test -f /opt/etc/sharefile.json; then
    sh "$work/release/update.sh"
else
    sh "$work/release/first-install.sh"
fi
echo "ShareFile $version installed."
