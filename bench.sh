#!/bin/sh
# bench.sh: iperf3 benchmark through tlswrapper
set -eu

SCRIPTDIR="$(cd "$(dirname "$0")" && pwd)"
BINARY="${SCRIPTDIR}/build/tlswrapper"
LOGDIR="${SCRIPTDIR}/build"

MUX_TLS_PORT=8443
# NETEM_DELAY=100ms
NETEM_DELAY=""
PIDS=""

if [ -n "${NETEM_DELAY}" ]; then
    # Add latency rootlessly by re-execing inside a user+network namespace.
    # Inside the new namespace we have CAP_NET_ADMIN and can apply tc netem.
    if [ -z "${BENCH_NETNS:-}" ]; then
        exec unshare --user --net --map-root-user -- env BENCH_NETNS=1 "$0" "$@"
    fi
    ip link set lo up
    # Delay only the multiplexd TLS transport on port 8443.
    # Leave the local iperf legs on loopback unshaped.
    tc qdisc add dev lo root handle 1: prio bands 2 \
        priomap 1 1 1 1 1 1 1 1 1 1 1 1 1 1 1 1
    tc qdisc add dev lo parent 1:1 handle 10: netem delay "${NETEM_DELAY}"
    tc filter add dev lo protocol ip parent 1:0 prio 1 u32 \
        match ip protocol 6 0xff \
        match ip dport "${MUX_TLS_PORT}" 0xffff \
        flowid 1:1
    tc filter add dev lo protocol ip parent 1:0 prio 1 u32 \
        match ip protocol 6 0xff \
        match ip sport "${MUX_TLS_PORT}" 0xffff \
        flowid 1:1
fi

cleanup() {
    # shellcheck disable=SC2086
    [ -n "$PIDS" ] && kill $PIDS 2>/dev/null || true
    sleep 0.2
    [ -n "$PIDS" ] && kill -9 $PIDS 2>/dev/null || true
    wait 2>/dev/null || true
}

trap cleanup EXIT INT TERM

# Generate certificates if not present
if [ ! -f "${SCRIPTDIR}/server-cert.pem" ] || [ ! -f "${SCRIPTDIR}/client-cert.pem" ]; then
    printf 'Generating certificates...\n'
    (cd "${SCRIPTDIR}" && timeout 30 "${BINARY}" -gencerts client,server)
fi

# Start iperf3 server on port 5201
printf 'Starting iperf3 server...\n'
iperf3 -s -p 5201 >"${LOGDIR}/iperf3-server.log" 2>&1 &
PIDS="$!"

# Start tlswrapper server
printf 'Starting tlswrapper server...\n'
"${BINARY}" -c "${SCRIPTDIR}/server.json" >"${LOGDIR}/tlswrapper-server.log" 2>&1 &
PIDS="$PIDS $!"

# Start tlswrapper client
printf 'Starting tlswrapper client...\n'
"${BINARY}" -c "${SCRIPTDIR}/client.json" >"${LOGDIR}/tlswrapper-client.log" 2>&1 &
PIDS="$PIDS $!"

sleep 1

# Run iperf3 uplink benchmark
printf 'Running iperf3 uplink benchmark...\n'
iperf3 -c 127.0.0.1 -p 5202 -t 30 >"${LOGDIR}/iperf3-uplink.log"

# Run iperf3 downlink benchmark
printf 'Running iperf3 downlink benchmark...\n'
iperf3 -c 127.0.0.1 -p 5202 -R -t 30 >"${LOGDIR}/iperf3-downlink.log"

# Run iperf3 bidirectional benchmark
printf 'Running iperf3 bidirectional benchmark...\n'
iperf3 -c 127.0.0.1 -p 5202 --bidir -t 30 >"${LOGDIR}/iperf3-bidir.log"

# Run iperf3 parallel benchmark
printf 'Running iperf3 parallel benchmark...\n'
iperf3 -c 127.0.0.1 -p 5202 --bidir -P 10 -t 30 >"${LOGDIR}/iperf3-parallel.log"
