#!/bin/sh -e

if [ $# = 0 ]; then
    echo "usage: $0 peer1 [peer2 [...]]"
    exit 1
fi

if [ -z "$SSLNAME" ]; then
    SSLNAME="example.com"
fi

if [ -z "$RSABITS" ]; then
    RSABITS="4096"
fi

while [ -n "$1" ]; do
    NAME="$1"
    echo "+ ${NAME}"

    openssl req -newkey "rsa:${RSABITS}" \
        -new -nodes -x509 \
        -days 36500 \
        -out "${NAME}-cert.pem" \
        -keyout "${NAME}-key.pem" \
        -subj "/C=US/ST=California/L=Mountain View/O=Your Organization/OU=Your Unit/CN=${SSLNAME}" \
        -addext "subjectAltName = DNS:${SSLNAME}"

    shift
done
