# tlswrapper

[![MIT License](https://img.shields.io/github/license/hexian000/tlswrapper)](https://github.com/hexian000/tlswrapper/blob/master/LICENSE)
[![Build](https://github.com/hexian000/tlswrapper/actions/workflows/build.yaml/badge.svg)](https://github.com/hexian000/tlswrapper/actions/workflows/build.yaml)
[![Go Report Card](https://goreportcard.com/badge/github.com/hexian000/tlswrapper/v4)](https://goreportcard.com/report/github.com/hexian000/tlswrapper/v4)
[![Downloads](https://img.shields.io/github/downloads/hexian000/tlswrapper/total.svg)](https://github.com/hexian000/tlswrapper/releases)
[![Release](https://img.shields.io/github/release/hexian000/tlswrapper.svg?style=flat)](https://github.com/hexian000/tlswrapper/releases)

Wrap any TCP-based service with multiplexed mutual TLS tunnels.

Status: **Stable**

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

- Multiplexed: All traffic goes over one TCP connection.
- Mutual Forwarded: Each peer can listen from and connect to the other peer simultaneously over the same underlying connection.
- Secured: All traffic is optionally protected by [mutual authenticated TLS](https://en.wikipedia.org/wiki/Mutual_authentication#mTLS).
- Long-Term Supported: Follow the latest releases of the dependent projects. Even if we don't make any changes, the binary release will be rebuilt at least once a year.

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
|          TCP traffic          |
+-------------------------------+
|   gRPC / HTTP/2 multiplexing  |
+-------------------------------+
|   mutual TLS 1.3 (optional)   |
+-------------------------------+
|  TCP/IP (untrusted network)   |
+-------------------------------+
```

## Authentication Model

Like SSH, each peer should have a key pair (X.509 certificate + PKCS #8 private key) and an authorized list. Only certificates in the authorized list (or signed by any authorized certificate) can communicate with the peer.

TLS behavior is based on "crypto/tls" library in [Go](https://github.com/golang/go).

## Quick Start

### Generating Key Pair

```sh
# generate self-signed certificates (default: RSA-4096)
./tlswrapper -gencerts client,server
# client-cert.pem, client-key.pem, server-cert.pem, server-key.pem

# choose a different key type or size
./tlswrapper -gencerts client,server -keytype ecdsa -keysize 256
./tlswrapper -gencerts client,server -keytype ed25519

# set the SNI (Subject / SAN) embedded in the certificate
./tlswrapper -gencerts server -sni example.com

# sign a peer certificate with an existing CA key pair
./tlswrapper -gencerts peer -sign ca
```

`-keytype` accepts `rsa` (default), `ecdsa`, or `ed25519`. `-keysize` sets the key size (RSA: bits, ECDSA: 224/256/384/521); `0` uses a safe default for the chosen type.

Adding a certificate to `"authcerts"` will allow all certificates signed by it.

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
