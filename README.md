# tlswrapper

[![MIT License](https://img.shields.io/github/license/hexian000/tlswrapper)](https://github.com/hexian000/tlswrapper/blob/master/LICENSE)
[![Go Report Card](https://goreportcard.com/badge/github.com/hexian000/tlswrapper)](https://goreportcard.com/report/github.com/hexian000/tlswrapper)
[![Release](https://img.shields.io/github/release/hexian000/tlswrapper.svg?style=flat)](https://github.com/hexian000/tlswrapper/releases)

## What for

Wrap your TCP-based service with multiplexed mutual TLS tunnels. 

Creating multiplexed TCP tunnels is generally not a good idea, see [Head-of-line blocking](https://en.wikipedia.org/wiki/Head-of-line_blocking). Make sure you have good reason to do so.

```
       Trusted      |     Untrusted    |     Trusted
+--------+    +------------+    +------------+    +--------+
| Client |-n->| tlswrapper |-1->| tlswrapper |-n->| Server |
+--------+    +------------+    +------------+    +--------+
+--------+    +------------+    +------------+    +--------+
| Server |<-n-| tlswrapper |-1->| tlswrapper |<-n-| Client |
+--------+    +------------+    +------------+    +--------+
```

## Protocol Stack

```
+-------------------------------+
|          TCP traffic          |
+-------------------------------+
|   yamux stream multiplexing   |
+-------------------------------+
|        mutual TLS 1.3         |
+-------------------------------+
|  TCP/IP (untrusted network)   |
+-------------------------------+
```


## Authentication Model

Like SSH, each peer needs to generate a key pair(certificate + private key). Only certificates in a peer's authorized certificates list can communicate with this peer.

This behavior is based on golang's TLS 1.3 implementation.

By default, all certificates are self-signed. This will not reduce security. 

## Quick Start

### 1. Generate key pair with OpenSSL:

```sh
./gencerts.sh client server
```

### 2. Create "config.json"

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

see [source code](config.go) for complete document

see [config.json](config.json) for example config file

### 3. Start

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
