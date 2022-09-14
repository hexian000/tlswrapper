#!/bin/sh -e

cd "$(dirname "$0")"
mkdir -p build

VERSION=""
if git rev-parse --git-dir >/dev/null 2>&1; then
    VERSION="$(git tag --points-at HEAD)"
    if [ -z "${VERSION}" ]; then
        VERSION="git-$(git rev-parse --short HEAD)"
    fi
    if ! git diff-index --quiet HEAD --; then
        VERSION="${VERSION}+"
    fi
fi
echo "+ version: ${VERSION}"

GOFLAGS="-trimpath -mod vendor"
LDFLAGS="-s -w"
if [ -n "${VERSION}" ]; then
    LDFLAGS="${LDFLAGS} -X main.version=${VERSION}"
fi

export CGO_ENABLED=0

case "$1" in
"x")
    # cross build for all supported targets
    # not supported targets are likely to work
    set -x
    GOOS="linux" GOARCH="mipsle" GOMIPS="softfloat" nice go build ${GOFLAGS} -ldflags "${LDFLAGS}" -o build/tlswrapper.linux-mipsle
    GOOS="linux" GOARCH="arm" GOARM=7 nice go build ${GOFLAGS} -ldflags "${LDFLAGS}" -o build/tlswrapper.linux-armv7
    GOOS="linux" GOARCH="arm64" nice go build ${GOFLAGS} -ldflags "${LDFLAGS}" -o build/tlswrapper.linux-arm64
    GOOS="linux" GOARCH="amd64" nice go build ${GOFLAGS} -ldflags "${LDFLAGS}" -o build/tlswrapper.linux-amd64
    GOOS="windows" GOARCH="amd64" nice go build ${GOFLAGS} -ldflags "${LDFLAGS}" -o build/tlswrapper.windows-amd64.exe
    ;;
*)
    # build for native system only
    set -x
    nice go build ${GOFLAGS} -ldflags "${LDFLAGS}" -o build/tlswrapper
    ;;
esac
