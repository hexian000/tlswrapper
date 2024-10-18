#!/bin/sh
cd "$(dirname "$0")"
mkdir -p build

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
echo "+ version: ${VERSION}"

set -e
MODROOT="./v3"
PACKAGE="./cmd/tlswrapper"
OUT="$(realpath ./build)/tlswrapper"
GOFLAGS="-trimpath"
GCFLAGS=""
LDFLAGS="-X github.com/hexian000/tlswrapper/v3.Version=${VERSION}"

cd "${MODROOT}" && go mod vendor
case "$1" in
"all")
    # cross build for all supported platforms
    # not listed platforms are likely to work
    GOFLAGS="${GOFLAGS} -buildmode=pie"
    LDFLAGS="${LDFLAGS} -s -w"
    CGO_ENABLED=1
    set -x
    GOOS="windows" GOARCH="amd64" \
        nice go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUT}.windows-amd64.exe" "${PACKAGE}"
    # static
    GOFLAGS="${GOFLAGS} -buildmode=exe"
    LDFLAGS="${LDFLAGS} -s -w"
    CGO_ENABLED=0
    set -x
    GOOS="linux" GOARCH="mipsle" GOMIPS="softfloat" \
        nice go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUT}.linux-mipsle" "${PACKAGE}"
    GOOS="linux" GOARCH="arm" GOARM=7 \
        nice go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUT}.linux-armv7" "${PACKAGE}"
    GOOS="linux" GOARCH="arm64" \
        nice go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUT}.linux-arm64" "${PACKAGE}"
    GOOS="linux" GOARCH="amd64" \
        nice go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUT}.linux-amd64" "${PACKAGE}"

    ;;
"x")
    # external toolchain, environment vars need to be set
    GOFLAGS="${GOFLAGS} -buildmode=pie"
    LDFLAGS="${LDFLAGS} -s -w"
    CGO_ENABLED=1
    set -x
    nice go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUT}.${OUTEXT}" "${PACKAGE}"
    ;;
"s")
    GOFLAGS="${GOFLAGS} -buildmode=exe"
    LDFLAGS="${LDFLAGS} -s -w"
    CGO_ENABLED=0
    set -x
    nice go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUT}" "${PACKAGE}"
    ls -lh "${OUT}"
    ;;
"r")
    GOFLAGS="${GOFLAGS} -buildmode=pie"
    LDFLAGS="${LDFLAGS} -s -w"
    CGO_ENABLED=1
    set -x
    nice go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUT}" "${PACKAGE}"
    ls -lh "${OUT}"
    ;;
"p")
    set -x
    nice go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUT}" "${PACKAGE}"
    ls -lh "${OUT}"
    ;;
"d")
    GOFLAGS="${GOFLAGS} -race"
    GCFLAGS="${GCFLAGS} -N -l"
    set -x
    go mod tidy && go fmt ./...
    nice go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUT}" "${PACKAGE}"
    ls -lh "${OUT}"
    ;;
*)
    set -x
    nice go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUT}" "${PACKAGE}"
    ls -lh "${OUT}"
    ;;
esac
