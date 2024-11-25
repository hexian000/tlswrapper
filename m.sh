#!/bin/sh
# make.sh: in-tree build script for convenience
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
mkdir -p build
OUTDIR="$(realpath ./build)"
MODROOT="./v3"
PACKAGE="./cmd/tlswrapper"
GOFLAGS="${GOFLAGS} -buildmode=exe -mod=readonly -trimpath -v"
GCFLAGS="${GCFLAGS}"
LDFLAGS="-X github.com/hexian000/tlswrapper/v3.Version=${VERSION}"

cd "${MODROOT}"
go mod vendor
case "$1" in
"c")
    # clean artifacts
    rm -rf build
    ;;
"p")
    # build for profiling
    set -x
    CGO_ENABLED=0 \
        go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUTDIR}/" "${PACKAGE}"
    ls -lh "${OUTDIR}/"
    ;;
"r")
    # build for release
    LDFLAGS="${LDFLAGS} -s -w"
    set -x
    CGO_ENABLED=0 \
        go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUTDIR}/" "${PACKAGE}"
    ls -lh "${OUTDIR}/"
    ;;
"d")
    # build for debug
    GOFLAGS="${GOFLAGS} -race"
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
