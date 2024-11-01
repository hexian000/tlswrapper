# tlswrapper

[![MIT License](https://img.shields.io/github/license/hexian000/tlswrapper)](https://github.com/hexian000/tlswrapper/blob/master/LICENSE)
[![Build](https://github.com/hexian000/tlswrapper/actions/workflows/build.yaml/badge.svg)](https://github.com/hexian000/tlswrapper/actions/workflows/build.yaml)
[![Go Report Card](https://goreportcard.com/badge/github.com/hexian000/tlswrapper/v3)](https://goreportcard.com/report/github.com/hexian000/tlswrapper/v3)
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
- Incompatible: Always enforce the latest TLS version.

*In terms of performance, creating multiplexed TCP tunnels is generally not a good idea, see [Head-of-line blocking](https://en.wikipedia.org/wiki/Head-of-line_blocking). Make sure you have a good reason to do so.*

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
|   yamux stream multiplexing   |
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

**client.json**

```json
{
    "peers": {
        "tlswrapper server": {
            "addr": "example.com:38000",
            "listen": "127.0.0.1:8080",
            "service": "myhttp"
        }
    },
    "certs": [
        {
            "cert": "@client-cert.pem",
            "key": "@client-key.pem"
        }
    ],
    "authcerts": [
        "@server-cert.pem"
    ]
}
```

**server.json**

```json
{
    "muxlisten": "0.0.0.0:38000",
    "services": {
        "myhttp": "127.0.0.1:80"
    },
    "certs": [
        {
            "cert": "@server-cert.pem",
            "key": "@server-key.pem"
        }
    ],
    "authcerts": [
        "@client-cert.pem"
    ]
}
```

For complex cases, see the [full example](https://github.com/hexian000/tlswrapper/wiki/Configuration-Example).

#### Notes

Feel free to add more services/peers, or bring up forwards/reverses between the same instances.

- "peername": local peer name
- "muxlisten": listener bind address
- "services": local service forwards
- "services[\*]": local service dial address
- "peers": named peers to that need to keep connected
- "peers[\*].addr": dial address
- "peers[\*].listen": listen for port forwarding
- "peers[\*].service": the service name we ask the peer for
- "certs": local certificates
- "certs[\*].cert": PEM encoded certificate (use "@filename" to read external file, same below)
- "certs[\*].key": PEM encoded private key
- "authcerts": peer authorized certificates list, bundles are supported
- "authcerts[\*].cert": PEM encoded certificate

See [source code](v3/config/config.go) for a complete list of all available options.

See [config.json](config.json) for example config file.

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
# checkout a tagged version
git checkout v3.0
# build release binary
./make.sh r

# or install the latest development version
go install github.com/hexian000/tlswrapper/v3/cmd/tlswrapper@latest
```

## Credits

- [go](https://github.com/golang/go)
- [gosnippets](https://github.com/hexian000/gosnippets)
- [yamux](https://github.com/hashicorp/yamux)
