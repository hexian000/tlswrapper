#!/bin/sh
cd "$(dirname "$0")"
mkdir -p build

VERSION="dev"
if git rev-parse --git-dir >/dev/null 2>&1; then
    VERSION="$(git tag --points-at HEAD)"
    if [ -z "${VERSION}" ]; then
        VERSION="git-$(git rev-parse --short HEAD)"
    fi
    git update-index --refresh -q
    if ! git diff-index --quiet HEAD --; then
        VERSION="${VERSION}+"
    fi
fi
echo "+ version: ${VERSION}"

set -e
MODROOT="./v2"
PACKAGE="./cmd/tlswrapper"
OUT="$(realpath ./build)/tlswrapper"
GOFLAGS="-trimpath -mod vendor"
GCFLAGS=""
LDFLAGS="-X github.com/hexian000/tlswrapper/v2.Version=${VERSION}"

cd "${MODROOT}"
case "$1" in
"x")
    # cross build for all supported targets
    # not listed targets are likely to work
    LDFLAGS="${LDFLAGS} -s -w"
    set -x
    CGO_ENABLED=0 GOOS="linux" GOARCH="mipsle" GOMIPS="softfloat" \
        nice go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUT}.linux-mipsle" "${PACKAGE}"
    CGO_ENABLED=0 GOOS="linux" GOARCH="arm" GOARM=7 \
        nice go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUT}.linux-armv7" "${PACKAGE}"
    CGO_ENABLED=0 GOOS="linux" GOARCH="arm64" \
        nice go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUT}.linux-arm64" "${PACKAGE}"
    CGO_ENABLED=0 GOOS="linux" GOARCH="amd64" \
        nice go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUT}.linux-amd64" "${PACKAGE}"
    CGO_ENABLED=0 GOOS="windows" GOARCH="amd64" \
        nice go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUT}.windows-amd64.exe" "${PACKAGE}"
    ;;
race)
    GOFLAGS="${GOFLAGS} -race"
    GCFLAGS="${GCFLAGS} -N -l"
    set -x
    CGO_ENABLED=1 \
        nice go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUT}" "${PACKAGE}"
    ls -lh "${OUT}"
    ;;
asan)
    GOFLAGS="${GOFLAGS} -asan"
    GCFLAGS="${GCFLAGS} -N -l"
    set -x
    CGO_ENABLED=1 \
        nice go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUT}" "${PACKAGE}"
    ls -lh "${OUT}"
    ;;
msan)
    GOFLAGS="${GOFLAGS} -msan"
    GCFLAGS="${GCFLAGS} -N -l"
    set -x
    CGO_ENABLED=1 CC=clang \
        nice go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUT}" "${PACKAGE}"
    ls -lh "${OUT}"
    ;;
*)
    set -x
    CGO_ENABLED=0 \
        nice go build ${GOFLAGS} -gcflags "${GCFLAGS}" -ldflags "${LDFLAGS}" \
        -o "${OUT}" "${PACKAGE}"
    ls -lh "${OUT}"
    ;;
esac