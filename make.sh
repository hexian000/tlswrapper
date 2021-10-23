#!/bin/sh -eux

cd "$(dirname "$0")"
mkdir -p build

export CGO_ENABLED=0
GOFLAGS="-trimpath"
GOOS="linux" GOARCH="arm64" nice go build ${GOFLAGS} -o build/tlswrapper_arm64
GOOS="linux" GOARCH="arm" GOARM=7 nice go build ${GOFLAGS} -o build/tlswrapper_armv7
GOOS="linux" GOARCH="amd64" nice go build ${GOFLAGS} -o build/tlswrapper_amd64
GOOS="windows" GOARCH="amd64" nice go build ${GOFLAGS} -o build/tlswrapper_amd64.exe
