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
    - [Notes](#notes)
  - [Start](#start)
- [Building/Installing from Source](#buildinginstalling-from-source)
- [Credits](#credits)

## Features

- Multiplexed: All traffic goes over one TCP connection.
- Mutual Forwarded: Each peer can listen from and connect to the other peer simultaneously over the same underlying connection.
- Secured: All traffic is optionally protected by [mutual authenticated TLS](https://en.wikipedia.org/wiki/Mutual_authentication#mTLS).
- Long-Term Supported: Follow the latest releases of the dependent projects. Even if we don't make any changes, the binary release will be rebuilt at least once a year.

*Note: tlswrapper is designed as an inconspicuous secure communication tunnel. This may increase latency in some scenarios, see [Head-of-line blocking](https://en.wikipedia.org/wiki/Head-of-line_blocking).*

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
# generate self-signed certificates
./tlswrapper -gencerts client,server
# client-cert.pem, client-key.pem, server-cert.pem, server-key.pem
```

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

#### Notes

Feel free to add more services, or bring up forwards/reverses between the same instances.

- `"type"`: config format identifier, must be `"application/x-tlswrapper-config; version=4"`
- `"service.id"`: self identity announced in the handshake
- `"mux_listen"`: address to accept inbound mux connections (server mode)
- `"connect"`: forwarding target for inbound application streams
- `"tls.cert"`: PEM certificate (use `"@filename"` to read from file at startup, same below)
- `"tls.key"`: PEM private key
- `"tls.authcerts"`: authorized peer certificates list; bundles are supported
- `"service.peers"`: peer identity to mux endpoint mapping (client mode)
- `"service.listen"`: peer identity to local listen address mapping
- `"mux.session_window"` / `"mux.stream_window"`: optional fixed HTTP/2 connection/stream flow-control windows in bytes; leave both at `0` to keep gRPC dynamic flow control, or set either one to pin that window size explicitly

See [source code](v4/config/config.go) for a complete list of all available options.

See [schema.json](v4/config/schema.json) for the full JSON Schema with field descriptions and defaults.

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
- [grpc-go](https://github.com/grpc/grpc-go)
