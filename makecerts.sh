#!/bin/sh -eu

if [ $# -lt 1 ]; then
    SSLNAME="example.com"
else
    SSLNAME="$1"
fi

openssl req -newkey rsa:8192 \
    -new -nodes -x509 \
    -days 36500 \
    -out cert.pem \
    -keyout key.pem \
    -subj "/C=US/ST=California/L=Mountain View/O=Your Organization/OU=Your Unit/CN=${SSLNAME}" \
    -addext "subjectAltName = DNS:${SSLNAME}"
