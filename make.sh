#!/bin/sh
cd "$(dirname "$0")"
mkdir -p build

VERSION=""
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
PACKAGE="./cmd/tlswrapper"
OUT="./build/tlswrapper"
GOFLAGS="-trimpath -mod vendor"
LDFLAGS="-s -w"
if [ -n "${VERSION}" ]; then
    LDFLAGS="${LDFLAGS} -X github.com/hexian000/tlswrapper.Version=${VERSION}"
fi

export CGO_ENABLED=0

case "$1" in
"x")
    # cross build for all supported targets
    # not listed targets are likely to work
    set -x
    GOOS="linux" GOARCH="mipsle" GOMIPS="softfloat" \
        nice go build ${GOFLAGS} -ldflags "${LDFLAGS}" -o "${OUT}.linux-mipsle" "${PACKAGE}"
    GOOS="linux" GOARCH="arm" GOARM=7 \
        nice go build ${GOFLAGS} -ldflags "${LDFLAGS}" -o "${OUT}.linux-armv7" "${PACKAGE}"
    GOOS="linux" GOARCH="arm64" \
        nice go build ${GOFLAGS} -ldflags "${LDFLAGS}" -o "${OUT}.linux-arm64" "${PACKAGE}"
    GOOS="linux" GOARCH="amd64" \
        nice go build ${GOFLAGS} -ldflags "${LDFLAGS}" -o "${OUT}.linux-amd64" "${PACKAGE}"
    GOOS="windows" GOARCH="amd64" \
        nice go build ${GOFLAGS} -ldflags "${LDFLAGS}" -o "${OUT}.windows-amd64.exe" "${PACKAGE}"
    ;;
*)
    # debug build for native system only
    set -x
    nice go build ${GOFLAGS} -o "${OUT}" "${PACKAGE}"
    ;;
esac
