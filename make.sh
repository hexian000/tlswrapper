#!/bin/sh
cd "$(dirname "$0")"

VERSION="dev"
if git rev-parse --git-dir >/dev/null 2>&1; then
    VERSION="$(git tag --points-at HEAD)"
    if [ -z "${VERSION}" ]; then
        VERSION="git-$(git rev-parse --short HEAD)"
    fi
    if ! git diff --quiet HEAD --; then
        VERSION="${VERSION}+"
    fi
fi

set -e
OUTDIR="$(realpath ./build)"
MODROOT="./v3"
PACKAGE="./cmd/tlswrapper"
GOFLAGS="${GOFLAGS} -v -trimpath"
GCFLAGS="${GCFLAGS}"
LDFLAGS="-X github.com/hexian000/tlswrapper/v3.Version=${VERSION}"

cd "${MODROOT}"
go mod vendor
case "$1" in
"r")
    GOFLAGS="-buildmode=exe ${GOFLAGS}"
    LDFLAGS="${LDFLAGS} -s -w"
    set -x
    CGO_ENABLED=0 \
        go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUTDIR}/" "${PACKAGE}"
    ls -lh "${OUTDIR}/"
    ;;
"d")
    GOFLAGS="-buildmode=exe -race ${GOFLAGS}"
    GCFLAGS="${GCFLAGS} -N -l"
    set -x
    go fmt ./... && go mod tidy
    CGO_ENABLED=1 \
        go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUTDIR}/" "${PACKAGE}"
    ls -lh "${OUTDIR}/"
    ;;
"t")
    set -x
    go fmt ./... && go mod tidy
    go test ./...
    ;;
*)
    set -x
    go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUTDIR}/" "${PACKAGE}"
    ls -lh "${OUTDIR}/"
    ;;
esac
