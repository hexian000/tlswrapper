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
# generate private root certificate
./tlswrapper -gencerts ca
# ca-cert.pem, ca-key.pem

# generate peer certificates
./tlswrapper -gencerts peer0,peer1,peer2 -sign ca
# peerN-cert.pem, peerN-key.pem
```

Adding a certificate to `"authcerts"` will allow all certificates signed by it (including itself).

### Creating Config Files

Example:

`client -> peer1 -> peer0 <- peer2 -> server`

For simpler cases, just remove the unused fragments.

**peer0.json**: If peer name is `peer2`, ask for `myhttp` service.

```json
{
  "peername": "peer0",
  "muxlisten": "0.0.0.0:38000",
  "services": {
    "myhttp-peer2": "127.0.0.1:8080"
  },
  "peers": {
    "peer2": {
      "listen": "127.0.0.1:8080",
      "service": "myhttp"
    }
  },
  "certs": [
    {
      "cert": "@peer0-cert.pem",
      "key": "@peer0-key.pem"
    }
  ],
  "authcerts": [
    "@ca-cert.pem"
  ]
}
```

**peer1.json**: Ask `peer0` for `myhttp-peer2` service.

```json
{
  "peername": "peer1",
  "peers": {
    "peer0": {
      "addr": "example.com:38000",
      "listen": "127.0.0.1:8080",
      "service": "myhttp-peer2"
    }
  },
  "certs": [
    {
      "cert": "@peer1-cert.pem",
      "key": "@peer1-key.pem"
    }
  ],
  "authcerts": [
    "@ca-cert.pem"
  ]
}
```

**peer2.json**: Connect to `peer0`.

```json
{
  "peername": "peer2",
  "services": {
    "myhttp": "127.0.0.1:8080"
  },
  "peers": {
    "peer0": {
      "addr": "example.com:38000"
    }
  },
  "certs": [
    {
      "cert": "@peer2-cert.pem",
      "key": "@peer2-key.pem"
    }
  ],
  "authcerts": [
    "@ca-cert.pem"
  ]
}
```

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

See [source code](v3/config.go) for a complete list of all available options.

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
# checkout tagged version
git checkout v3.0
# build with default configuration
./make.sh

# or install the latest development version
go install github.com/hexian000/tlswrapper/v3/cmd/tlswrapper@latest
```

## Credits

- [go](https://github.com/golang/go)
- [gosnippets](https://github.com/hexian000/gosnippets)
- [yamux](https://github.com/hashicorp/yamux)
