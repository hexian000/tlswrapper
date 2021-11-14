#!/bin/sh -eux

: wrt32x
ssh wrt -- killall -9 "tlswrapper" &

: tcloud
ssh tcloud -- systemctl --user restart tlswrapper --no-block &

: farter rpi
ssh rpi -- systemctl --user restart tlswrapper --no-block &

: "OK"
