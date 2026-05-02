# tlswrapper

[![MIT License](https://img.shields.io/github/license/hexian000/tlswrapper)](https://github.com/hexian000/tlswrapper/blob/master/LICENSE)
[![Build](https://github.com/hexian000/tlswrapper/actions/workflows/build.yaml/badge.svg)](https://github.com/hexian000/tlswrapper/actions/workflows/build.yaml)
[![Go Report Card](https://goreportcard.com/badge/github.com/hexian000/tlswrapper/v4)](https://goreportcard.com/report/github.com/hexian000/tlswrapper/v4)
[![Downloads](https://img.shields.io/github/downloads/hexian000/tlswrapper/total.svg)](https://github.com/hexian000/tlswrapper/releases)
[![Release](https://img.shields.io/github/release/hexian000/tlswrapper.svg?style=flat)](https://github.com/hexian000/tlswrapper/releases)

Wrap any TCP-based service with multiplexed mutual TLS tunnels.

- [Features](#features)
- [Protocol Stack](#protocol-stack)
- [Authentication Model](#authentication-model)
- [Quick Start](#quick-start)
  - [Generating Key Pair](#generating-key-pair)
  - [Creating Config Files](#creating-config-files)
  - [Start](#start)
- [Building/Installing from Source](#buildinginstalling-from-source)
- [Credits](#credits)

## Features

- Multiplexed: Multiple TCP streams share one long-lived transport connection.
- Bidirectional Forwarding: Each peer can expose local services and reach remote services over the same underlying connection.
- Named Peer Routing: Map peer identities to config-driven mux dial addresses and local listen addresses.
- TLS 1.3 or Plaintext: Protect traffic with [mutual authenticated TLS](https://en.wikipedia.org/wiki/Mutual_authentication#mTLS), or run without TLS on trusted links.
- Certificate Allowlist: Authorize exact peer certificates or certificates signed by an authorized certificate.
- Built-in Certificate Tooling: Generate RSA, ECDSA, or Ed25519 key pairs, either self-signed or signed by an existing key pair.
- Automatic Recovery: Config-driven tunnels with mux_connect redial with backoff, and configuration can be reloaded without restarting the process.
- Observability: Expose health checks, human-readable stats, Prometheus metrics, and recent events over the optional HTTP management API.
- Tunable Limits: Configure keepalive, timeouts, flow-control windows, session and stream limits, backlog, and connection throttling.

At runtime, tlswrapper keeps two tunnel lifecycles: config-driven tunnels loaded from configuration, and inbound ephemeral tunnels created for accepted mux connections. The latter are removed as soon as their underlying mux connection closes.

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

## Authentication Model

When TLS is enabled, tlswrapper uses mutual TLS: each peer presents an X.509 certificate and proves possession of the matching PKCS #8 private key during the handshake.

Trust is configured through `authcerts`. Each entry can be either:

- A specific peer certificate, for direct certificate pinning.
- A signing certificate, to trust any peer certificate issued by that signer.

Handshake and certificate verification are delegated to Go's [crypto/tls](https://pkg.go.dev/crypto/tls) implementation. A connection is accepted only if the remote certificate chain validates against the local `authcerts` pool.

If the `tls` section is omitted, tlswrapper runs in plaintext mode and does not provide certificate-based peer authentication. Use that mode only on links you already trust.

## Quick Start

### Generating Key Pair

```sh
# generate self-signed certificates (default: RSA-4096)
./tlswrapper -gencerts client,server
# client-cert.pem, client-key.pem, server-cert.pem, server-key.pem

# set the SNI (Subject / SAN) embedded in the certificate
./tlswrapper -gencerts server -sni example.com

# generate a self-signed CA key pair
./tlswrapper -gencerts ca -sni ca.example.com
# ca-cert.pem, ca-key.pem

# sign a peer certificate with that CA key pair
./tlswrapper -gencerts peer -sign ca
```

`-keytype` accepts `rsa` (default), `ecdsa`, or `ed25519`. `-keysize` sets the key size (RSA: bits, ECDSA: 224/256/384/521); `0` uses a safe default for the chosen type.

Adding `ca-cert.pem` to `"authcerts"` will allow peer certificates signed by that CA.

### Creating Config Files

**Connection Graph**

`http client -> tlswrapper client -> tlswrapper server -> http server`

**server.json**

```json
{
    "type": "application/x-tlswrapper-config; version=4",
    "mux_listen": "0.0.0.0:38000",
    "connect": "127.0.0.1:80",
    "tls": {
        "cert": "@server-cert.pem",
        "key": "@server-key.pem",
        "authcerts": [
            "@client-cert.pem"
        ]
    },
    "service": {
        "id": "server"
    }
}
```

**client.json**

```json
{
    "type": "application/x-tlswrapper-config; version=4",
    "tls": {
        "cert": "@client-cert.pem",
        "key": "@client-key.pem",
        "authcerts": [
            "@server-cert.pem"
        ]
    },
    "service": {
        "id": "client",
        "peers": {
            "server": "example.com:38000"
        },
        "listen": {
            "server": "127.0.0.1:8080"
        }
    }
}
```

For complex cases, see the [full example](https://github.com/hexian000/tlswrapper/wiki/Configuration-Example).

For field descriptions, defaults, and the full configuration format, see [schema.json](v4/config/schema.json).

### Start

```sh
./tlswrapper -c server.json

./tlswrapper -c client.json
```

## Building/Installing from Source

```sh
# get source code
git clone https://github.com/hexian000/tlswrapper.git
cd tlswrapper
# build for debug
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
