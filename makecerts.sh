#!/bin/sh -e

if [ -z "$SSLNAME" ]; then
    SSLNAME="example.com"
fi

while [ -n "$1" ]; do
    NAME="$1"
    echo "+ ${NAME}"

    openssl req -newkey rsa:8192 \
        -new -nodes -x509 \
        -days 36500 \
        -out cert.pem \
        -keyout key.pem \
        -subj "/C=US/ST=California/L=Mountain View/O=Your Organization/OU=Your Unit/CN=${SSLNAME}" \
        -addext "subjectAltName = DNS:${SSLNAME}"

    shift
done
