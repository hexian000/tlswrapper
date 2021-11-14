#!/bin/sh -eux

#. ~/proxy.conf
#git pull --ff-only -p

./make.sh x

cd build
if [ -f tlswrapper.sha256 ] && sha256sum -c tlswrapper.sha256; then
    : "Already up-to-date."
    exit 0
fi

: wrt32x
scp tlswrapper.linux-armv7 wrt:"/root/tlswrapper/"
ssh wrt -- mv "/root/tlswrapper/tlswrapper.linux-armv7" "/root/tlswrapper/tlswrapper"

: farter rpi
scp -C tlswrapper.linux-armv7 rpi:"~/.local/bin/"
ssh rpi -- mv "~/.local/bin/tlswrapper.linux-armv7" "~/.local/bin/tlswrapper"

: tcloud
scp -C tlswrapper.linux-amd64 tcloud:"~/.local/bin/"
ssh tcloud -- mv "~/.local/bin/tlswrapper.linux-amd64" "~/.local/bin/tlswrapper"

sha256sum tlswrapper.linux-arm64 tlswrapper.linux-armv7 tlswrapper.linux-amd64 tlswrapper.windows-amd64.exe >tlswrapper.sha256
: "OK"
