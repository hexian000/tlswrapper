#!/bin/sh -eux


GOOS="linux" GOARCH="arm" GOARM=7 nice go build -trimpath -ldflags "-X main.version=dev-$(date -Iseconds)" -o build/tlswrapper.linux-armv7
scp build/tlswrapper.linux-armv7 rpi:~/.local/bin/
ssh rpi -- "mv ~/.local/bin/tlswrapper.linux-armv7 ~/.local/bin/tlswrapper"
