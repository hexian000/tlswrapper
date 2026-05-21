# tlswrapper

[![MIT License](https://img.shields.io/github/license/hexian000/tlswrapper)](https://github.com/hexian000/tlswrapper/blob/master/LICENSE)
[![Build](https://github.com/hexian000/tlswrapper/actions/workflows/build.yaml/badge.svg)](https://github.com/hexian000/tlswrapper/actions/workflows/build.yaml)
[![Go Report Card](https://goreportcard.com/badge/github.com/hexian000/tlswrapper/v4)](https://goreportcard.com/report/github.com/hexian000/tlswrapper/v4)
[![Downloads](https://img.shields.io/github/downloads/hexian000/tlswrapper/total.svg)](https://github.com/hexian000/tlswrapper/releases)
[![Release](https://img.shields.io/github/release/hexian000/tlswrapper.svg?style=flat)](https://github.com/hexian000/tlswrapper/releases)

Wrap TCP-based services with multiplexed mutual TLS tunnels over TCP or QUIC.

- [Features](#features)
- [Protocol Stack](#protocol-stack)
- [Authentication Model](#authentication-model)
- [Quick Start](#quick-start)
  - [Generating Key Pairs](#generating-key-pairs)
  - [Creating Config Files](#creating-config-files)
  - [Starting](#starting)
- [Building or Installing from Source](#building-or-installing-from-source)
- [Credits](#credits)

## Features

- **Multiplexed**: Multiple TCP streams share a single long-lived transport connection.
- **Bidirectional Forwarding**: Each peer can expose local services and reach remote services over the same underlying connection.
- **Pluggable Transport**: Choose between `h2mux` (gRPC over TCP+TLS, default) and `h3mux` (QUIC+TLS) via the `mux_protocol` config key.
- **mTLS 1.3 Security**: Protect traffic with [mutual authenticated TLS](https://en.wikipedia.org/wiki/Mutual_authentication#mTLS), or run in plaintext on trusted links (h2mux only).
- **Built-in Certificate Tool**: Generate RSA, ECDSA, or Ed25519 key pairs, either self-signed or signed by an existing key pair.
- **Certificate Allowlist**: Authorize exact peer certificates or any certificates signed by an authorized issuer. System CAs are never consulted.
- **Named Peer Routing**: Map peer identities to config-driven mux dial targets and local listen addresses.
- **Automatic Recovery**: Config-driven tunnels can redial mux_connect targets with backoff on disconnect.
- **Hot Reloading**: Apply updated configuration at runtime via SIGHUP or the HTTP management API without restarting the process.
- **Tunable Limits**: Configure keepalive, timeouts, flow-control windows, session and stream limits, backlog, and connection throttling.
- **Observability**: Expose health checks, human-readable stats, Prometheus metrics, and recent events through the optional HTTP management API.
- **systemd Integration**: Sends sd_notify Ready, Reloading, and Stopping state notifications when managed by systemd.

At runtime, tlswrapper maintains two tunnel lifecycles: config-driven tunnels loaded from configuration, and inbound ephemeral tunnels created for accepted mux connections. The latter are removed as soon as the underlying mux connection closes.

```
       Trusted      |     Untrusted    |     Trusted
+--------+    +------------+    +------------+    +--------+
| Client |-n->|            |    |            |-n->| Server |
+--------+    |            |    |            |    +--------+
              | tlswrapper |-1->| tlswrapper |
+--------+    |            |    |            |    +--------+
| Server |<-n-|            |    |            |<-n-| Client |
+--------+    +------------+    +------------+    +--------+
```

## Protocol Stack

**h2mux** (default, `"mux_protocol": "h2mux"`):
```
+-------------------------------+
|          TCP streams          |
+-------------------------------+
|   gRPC / HTTP/2 multiplexing  |
+-------------------------------+
|   mutual TLS 1.3 (optional)   |
+-------------------------------+
|  TCP/IP (untrusted network)   |
+-------------------------------+
```

**h3mux** (`"mux_protocol": "h3mux"`):
```
+-------------------------------+
|          TCP streams          |
+-------------------------------+
|    QUIC stream multiplexing   |
+-------------------------------+
|        mutual TLS 1.3         |
+-------------------------------+
|  UDP/IP (untrusted network)   |
+-------------------------------+
```

h3mux requires TLS to be configured and uses UDP on the untrusted side. It may perform better on high-latency or lossy links.

## Authentication Model

When TLS is enabled, tlswrapper uses mutual TLS: each peer presents an X.509 certificate and proves possession of the corresponding PKCS #8 private key during the handshake.

Trust is configured through `authcerts`. Each entry can be either:

- A specific peer certificate, for direct certificate pinning.
- A signing certificate, to trust any peer certificate issued by that signer.

The TLS handshake and certificate verification are delegated to Go's [crypto/tls](https://pkg.go.dev/crypto/tls) implementation. A connection is accepted only if the remote certificate chain validates against the local `authcerts` pool.

If the `tls` section is omitted, tlswrapper runs in plaintext mode and does not provide certificate-based peer authentication. Use that mode only on links you already trust.

## Quick Start

### Generating Key Pairs

```sh
# generate self-signed certificates (default: RSA-4096)
./tlswrapper -gencerts client,server
# client-cert.pem, client-key.pem, server-cert.pem, server-key.pem

# set the SNI value embedded in the certificate subject/SAN
./tlswrapper -gencerts server -sni example.com

# generate a self-signed CA key pair
./tlswrapper -gencerts ca -sni ca.example.com
# ca-cert.pem, ca-key.pem

# sign a peer certificate with that CA key pair
./tlswrapper -gencerts peer -sign ca
```

`-keytype` accepts `rsa` (default), `ecdsa`, or `ed25519`. `-keysize` sets the key size (RSA: bits, ECDSA: 224/256/384/521); `0` selects a safe default for the chosen type.

Adding `ca-cert.pem` to `"authcerts"` allows peer certificates signed by that CA.

### Creating Config Files

**Connection Graph**

`http client -> tlswrapper client -> tlswrapper server -> http server`

**server.json**

```json
{
    "mux_listen": "0.0.0.0:38000",
    "connect": "127.0.0.1:80",
    "tls": {
        "cert": "@server-cert.pem",
        "key": "@server-key.pem",
        "authcerts": [
            "@client-cert.pem"
        ]
    },
    "identity": {
        "claim": "server"
    }
}
```

**client.json**

```json
{
    "tls": {
        "cert": "@client-cert.pem",
        "key": "@client-key.pem",
        "authcerts": [
            "@server-cert.pem"
        ]
    },
    "identity": {
        "claim": "client",
        "mux_connect": [
            "example.com:38000"
        ],
        "listen": {
            "server": "127.0.0.1:8080"
        }
    }
}
```

To use QUIC instead of TCP, add `"mux_protocol": "h3mux"` to both config files and update `mux_listen` / `mux_connect` to the UDP port.

For complex cases, see the [full example](https://github.com/hexian000/tlswrapper/wiki/Configuration-Example).

For field descriptions, defaults, and the complete configuration format, see [schema.json](v4/config/schema.json).

### Starting

```sh
./tlswrapper -c server.json

./tlswrapper -c client.json
```

## Building or Installing from Source

```sh
# clone the source code
git clone https://github.com/hexian000/tlswrapper.git
cd tlswrapper
# build a debug binary
./m.sh d

# or install the latest development version
go install github.com/hexian000/tlswrapper/v4/cmd/tlswrapper@master
```

## Credits

- [go](https://github.com/golang/go)
- [gosnippets](https://github.com/hexian000/gosnippets)
- [Prometheus client_golang](https://github.com/prometheus/client_golang)
- [grpc-go](https://github.com/grpc/grpc-go)
- [protobuf-go](https://github.com/protocolbuffers/protobuf-go)
- [quic-go](https://github.com/quic-go/quic-go)
