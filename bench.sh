#!/bin/sh
# bench.sh: iperf3 benchmark through tlswrapper
set -eu

SCRIPTDIR="$(cd "$(dirname "$0")" && pwd)"
BINARY="${SCRIPTDIR}/build/tlswrapper"
LOGDIR="${SCRIPTDIR}/build"
PIDS=""

cleanup() {
    # shellcheck disable=SC2086
    [ -n "$PIDS" ] && kill $PIDS 2>/dev/null || true
    sleep 1
    [ -n "$PIDS" ] && kill -9 $PIDS 2>/dev/null || true
    wait 2>/dev/null || true
}

trap cleanup EXIT INT TERM

wait_port() {
    _host="$1"
    _port="$2"
    _tries=50
    while [ "$_tries" -gt 0 ]; do
        nc -z "$_host" "$_port" 2>/dev/null && return 0
        sleep 0.1
        _tries=$((_tries - 1))
    done
    printf 'timeout waiting for %s:%s\n' "$_host" "$_port" >&2
    return 1
}

# Generate certificates if not present
if [ ! -f "${SCRIPTDIR}/server-cert.pem" ] || [ ! -f "${SCRIPTDIR}/client-cert.pem" ]; then
    printf 'Generating certificates...\n'
    (cd "${SCRIPTDIR}" && timeout 30 "${BINARY}" --gencerts client,server)
fi

# Start iperf3 server on port 5201
printf 'Starting iperf3 server...\n'
iperf3 -s -p 5201 >"${LOGDIR}/iperf3-server.log" 2>&1 &
PIDS="$!"
wait_port 127.0.0.1 5201

# Start tlswrapper server
printf 'Starting tlswrapper server...\n'
"${BINARY}" -c "${SCRIPTDIR}/server.json" >"${LOGDIR}/tlswrapper-server.log" 2>&1 &
PIDS="$PIDS $!"
wait_port 127.0.0.1 8443

# Start tlswrapper client
printf 'Starting tlswrapper client...\n'
"${BINARY}" -c "${SCRIPTDIR}/client.json" >"${LOGDIR}/multiplexd-client.log" 2>&1 &
PIDS="$PIDS $!"
wait_port 127.0.0.1 5202

# Run iperf3 uplink benchmark
printf 'Running iperf3 uplink benchmark...\n'
iperf3 -c 127.0.0.1 -p 5202 -t 30 | tee "${LOGDIR}/iperf3-uplink.log"

# Run iperf3 downlink benchmark
printf 'Running iperf3 downlink benchmark...\n'
iperf3 -c 127.0.0.1 -p 5202 -R -t 30 | tee "${LOGDIR}/iperf3-downlink.log"

# Run iperf3 bidirectional benchmark
printf 'Running iperf3 bidirectional benchmark...\n'
iperf3 -c 127.0.0.1 -p 5202 --bidir -t 30 | tee "${LOGDIR}/iperf3-bidir.log"

# Run iperf3 parallel benchmark
printf 'Running iperf3 parallel benchmark...\n'
iperf3 -c 127.0.0.1 -p 5202 --bidir -P 10 -t 30 | tee "${LOGDIR}/iperf3-parallel.log"
