#!/bin/sh -e

if [ -z "$SSLNAME" ]; then
    SSLNAME="example.com"
fi

if [ ! -f ca-key.pem ]; then
    openssl genrsa 8192 >ca-key.pem
    openssl req -new -x509 -nodes -days 36500 \
        -key ca-key.pem \
        -out ca-cert.pem \
        -subj "/C=US/ST=California/L=Mountain View/O=Your Organization/OU=Your Unit/CN=${SSLNAME}" \
        -addext "subjectAltName = DNS:${SSLNAME}"
fi

while [ -n "$1" ]; do
    NAME="$1"

    openssl req -newkey rsa:8192 -nodes -days 36500 \
        -keyout "${NAME}-key.pem" \
        -out "${NAME}.csr" \
        -subj "/C=US/ST=California/L=Mountain View/O=Your Organization/OU=Your Unit/CN=${SSLNAME}" \
        -addext "subjectAltName = DNS:${SSLNAME}"

    openssl x509 -req -days 36500 -CAcreateserial \
        -in "${NAME}.csr" \
        -out "${NAME}-cert.pem" \
        -CA ca-cert.pem \
        -CAkey ca-key.pem
    shift
done
