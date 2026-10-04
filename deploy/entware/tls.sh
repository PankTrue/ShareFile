#!/bin/sh

# Keep the local CA stable so clients only need to trust it once.
sharefile_prepare_tls() (
    set -eu
    umask 077
    tls_dir=$1
    tls_ip=$2
    mkdir -p "$tls_dir"
    chmod 700 "$tls_dir"
    tls_work=$(mktemp -d "$tls_dir/.tls.XXXXXX")
    trap 'rm -rf "$tls_work"' 0

    if test ! -e "$tls_dir/lan-ca.pem" && test ! -e "$tls_dir/lan-ca-key.pem"; then
        openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
            -sha256 -days 3650 -subj '/CN=ShareFile LAN CA' \
            -addext 'basicConstraints=critical,CA:TRUE,pathlen:0' \
            -addext 'keyUsage=critical,keyCertSign,cRLSign' \
            -addext 'subjectKeyIdentifier=hash' \
            -keyout "$tls_work/ca-key.pem" -out "$tls_work/ca.pem"
        mv "$tls_work/ca-key.pem" "$tls_dir/lan-ca-key.pem"
        mv "$tls_work/ca.pem" "$tls_dir/lan-ca.pem"
    fi
    test -f "$tls_dir/lan-ca.pem" && test -f "$tls_dir/lan-ca-key.pem" || {
        echo 'Incomplete LAN CA: restore its certificate/key pair instead of replacing trusted identity.' >&2
        exit 1
    }
    openssl x509 -in "$tls_dir/lan-ca.pem" -checkend 86400 -noout
    openssl x509 -in "$tls_dir/lan-ca.pem" -pubkey -noout > "$tls_work/ca-cert.pub"
    openssl pkey -in "$tls_dir/lan-ca-key.pem" -pubout > "$tls_work/ca-key.pub"
    cmp -s "$tls_work/ca-cert.pub" "$tls_work/ca-key.pub" || {
        echo 'LAN CA certificate and key do not match.' >&2
        exit 1
    }
    chmod 600 "$tls_dir/lan-ca-key.pem"
    chmod 644 "$tls_dir/lan-ca.pem"

    if test -f "$tls_dir/admin-cert.pem" && test -f "$tls_dir/admin-key.pem" && \
        openssl x509 -in "$tls_dir/admin-cert.pem" -checkend 2592000 -noout >/dev/null && \
        openssl verify -purpose sslserver -verify_ip "$tls_ip" -CAfile "$tls_dir/lan-ca.pem" "$tls_dir/admin-cert.pem" >/dev/null; then
        openssl x509 -in "$tls_dir/admin-cert.pem" -pubkey -noout > "$tls_work/server-cert.pub"
        openssl pkey -in "$tls_dir/admin-key.pem" -pubout > "$tls_work/server-key.pub"
        if cmp -s "$tls_work/server-cert.pub" "$tls_work/server-key.pub"; then
            chmod 600 "$tls_dir/admin-key.pem"
            chmod 644 "$tls_dir/admin-cert.pem"
            exit 0
        fi
    fi

    openssl req -new -newkey ec -pkeyopt ec_paramgen_curve:prime256v1 -nodes \
        -subj "/CN=$tls_ip" -keyout "$tls_work/server-key.pem" -out "$tls_work/server.csr"
    cat > "$tls_work/server.ext" <<EOF
basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=serverAuth
subjectAltName=IP:$tls_ip
subjectKeyIdentifier=hash
authorityKeyIdentifier=keyid,issuer
EOF
    openssl x509 -req -in "$tls_work/server.csr" \
        -CA "$tls_dir/lan-ca.pem" -CAkey "$tls_dir/lan-ca-key.pem" \
        -set_serial "0x$(openssl rand -hex 16)" -sha256 -days 365 \
        -extfile "$tls_work/server.ext" -out "$tls_work/server-cert.pem"
    openssl verify -purpose sslserver -verify_ip "$tls_ip" -CAfile "$tls_dir/lan-ca.pem" "$tls_work/server-cert.pem"
    mv "$tls_work/server-key.pem" "$tls_dir/admin-key.pem"
    mv "$tls_work/server-cert.pem" "$tls_dir/admin-cert.pem"
    chmod 600 "$tls_dir/admin-key.pem"
    chmod 644 "$tls_dir/admin-cert.pem"
)
