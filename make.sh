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

if ! [ -d "${OUTDIR}" ]; then
    OUTDIR="$(realpath ./build)"
    mkdir -p "${OUTDIR}"
fi

if [ -z "${OUT}" ]; then
    OUT="tlswrapper"
fi

set -e
MODROOT="./v3"
PACKAGE="./cmd/tlswrapper"
GOFLAGS="${GOFLAGS} -trimpath"
GCFLAGS="${GCFLAGS}"
LDFLAGS="-X github.com/hexian000/tlswrapper/v3.Version=${VERSION}"

cd "${MODROOT}"
case "$1" in
"tlswrapper.android-arm64")
    GOFLAGS="-buildmode=pie ${GOFLAGS}"
    LDFLAGS="${LDFLAGS} -s -w"
    CGO_ENABLED=1 GOOS="android" GOARCH="arm64" \
        go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUTDIR}/$1" "${PACKAGE}"
    ;;
"tlswrapper.linux-amd64")
    GOFLAGS="-buildmode=exe ${GOFLAGS}"
    LDFLAGS="${LDFLAGS} -s -w"
    CGO_ENABLED=0 GOOS="linux" GOARCH="amd64" \
        go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUTDIR}/$1" "${PACKAGE}"
    ;;
"tlswrapper.linux-arm64")
    GOFLAGS="-buildmode=exe ${GOFLAGS}"
    LDFLAGS="${LDFLAGS} -s -w"
    CGO_ENABLED=0 GOOS="linux" GOARCH="arm64" \
        go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUTDIR}/$1" "${PACKAGE}"
    ;;
"tlswrapper.linux-armv7")
    GOFLAGS="-buildmode=exe ${GOFLAGS}"
    LDFLAGS="${LDFLAGS} -s -w"
    CGO_ENABLED=0 GOOS="linux" GOARCH="arm" GOARM=7 \
        go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUTDIR}/$1" "${PACKAGE}"
    ;;
"tlswrapper.linux-mipsle")
    GOFLAGS="-buildmode=exe ${GOFLAGS}"
    LDFLAGS="${LDFLAGS} -s -w"
    CGO_ENABLED=0 GOOS="linux" GOARCH="mipsle" GOMIPS="softfloat" \
        go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUTDIR}/$1" "${PACKAGE}"
    ;;
"tlswrapper.windows-amd64.exe")
    GOFLAGS="-buildmode=pie ${GOFLAGS}"
    LDFLAGS="${LDFLAGS} -s -w"
    CGO_ENABLED=1 GOOS="windows" GOARCH="amd64" \
        go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUTDIR}/$1" "${PACKAGE}"
    ;;
"tlswrapper-debug.linux-amd64")
    GOFLAGS="-buildmode=exe -race ${GOFLAGS}"
    GCFLAGS="${GCFLAGS} -N -l"
    CGO_ENABLED=1 GOOS="linux" GOARCH="amd64" \
        go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUTDIR}/$1" "${PACKAGE}"
    ;;
"tlswrapper-debug.linux-arm64")
    GOFLAGS="-buildmode=exe -race ${GOFLAGS}"
    GCFLAGS="${GCFLAGS} -N -l"
    CGO_ENABLED=1 GOOS="linux" GOARCH="arm64" \
        go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUTDIR}/$1" "${PACKAGE}"
    ;;
"d")
    GOFLAGS="-buildmode=exe -race ${GOFLAGS}"
    GCFLAGS="${GCFLAGS} -N -l"
    go mod tidy && go fmt ./...
    CGO_ENABLED=1 \
        go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUTDIR}/${OUT}" "${PACKAGE}"
    ls -lh "${OUTDIR}/${OUT}"
    ;;
*)
    go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUTDIR}/${OUT}" "${PACKAGE}"
    ls -lh "${OUTDIR}/${OUT}"
    ;;
esac
