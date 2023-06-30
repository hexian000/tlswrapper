# tlswrapper

[![MIT License](https://img.shields.io/github/license/hexian000/tlswrapper)](https://github.com/hexian000/tlswrapper/blob/master/LICENSE)
[![Build](https://github.com/hexian000/tlswrapper/actions/workflows/build.yaml/badge.svg)](https://github.com/hexian000/tlswrapper/actions/workflows/build.yaml)
[![Go Report Card](https://goreportcard.com/badge/github.com/hexian000/tlswrapper)](https://goreportcard.com/report/github.com/hexian000/tlswrapper)
[![Release](https://img.shields.io/github/release/hexian000/tlswrapper.svg?style=flat)](https://github.com/hexian000/tlswrapper/releases)

- [Features](#features)
- [Protocol Stack](#protocol-stack)
- [Authentication Model](#authentication-model)
- [Quick Start](#quick-start)
  - [Generate key pair with OpenSSL](#generate-key-pair-with-openssl)
  - [Create "config.json"](#create-configjson)
    - [Server](#server)
    - [Client](#client)
    - [Options](#options)
  - [Start](#start)
- [Build/Install](#buildinstall)
- [Credits](#credits)

## Features

Wrap your TCP-based service with multiplexed mutual TLS tunnels.

- Multiplexed: All traffic goes over one TCP connection.
- Secured: All traffic is optionally protected by mutual authenticated TLS.
- Mutual Forwarded: Each peer can listen and connect to the other peer simultaneously over the same underlying connection.

*In terms of performance, creating multiplexed TCP tunnels is generally not a good idea, see [Head-of-line blocking](https://en.wikipedia.org/wiki/Head-of-line_blocking). Make sure you have good reason to do so.*

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

Like SSH, each peer needs to generate a key pair(certificate + private key). Only certificates in a peer's authorized certificates list can communicate with this peer.

This behavior is based on golang's TLS 1.3 implementation.

By default, all certificates are self-signed. This will not reduce security. 

## Quick Start

### Generate key pair with OpenSSL

See [gencerts.sh](gencerts.sh).

```sh
./gencerts.sh client server
```

### Create "config.json"

#### Server

```json
{
  "tunnel": [
    {
      "muxlisten": "0.0.0.0:12345",
      "dial": "127.0.0.1:8080"
    }
  ],
  "cert": "server-cert.pem",
  "key": "server-key.pem",
  "authcerts": [
    "client-cert.pem"
  ]
}
```

#### Client

```json
{
  "tunnel": [
    {
      "listen": "127.0.0.1:8080",
      "muxdial": "example.com:12345"
    }
  ],
  "cert": "client-cert.pem",
  "key": "client-key.pem",
  "authcerts": [
    "server-cert.pem"
  ]
}
```

#### Options

- "tunnel": TLS tunnel configs
- "tunnel[\*].muxlisten": Server bind address for TLS connections
- "tunnel[\*].muxdial": Client dial address for TLS connections
- "tunnel[\*].listen": Listen for port forwarding
- "tunnel[\*].dial": The address we forward incoming connections to
- "cert": peer certificate
- "key": peer private key
- "authcerts": peer authorized certificates list, bundles are supported

See [source code](config.go) for complete list of all available options.

See [config.json](config.json) for example config file.

### Start

```sh
./tlswrapper -c config.json
```

You may also found the systemd user unit [tlswrapper.service](tlswrapper.service) is useful.

## Build/Install

```sh
# get source code
git clone https://github.com/hexian000/tlswrapper.git
cd tlswrapper
# build an executable for local system
./make.sh
```
or
```sh
go install github.com/hexian000/tlswrapper@latest
```

## Credits

- [go](https://github.com/golang/go)
- [yamux](https://github.com/hashicorp/yamux)
