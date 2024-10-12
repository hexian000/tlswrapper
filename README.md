# tlswrapper

[![MIT License](https://img.shields.io/github/license/hexian000/tlswrapper)](https://github.com/hexian000/tlswrapper/blob/master/LICENSE)
[![Build](https://github.com/hexian000/tlswrapper/actions/workflows/build.yaml/badge.svg)](https://github.com/hexian000/tlswrapper/actions/workflows/build.yaml)
[![Go Report Card](https://goreportcard.com/badge/github.com/hexian000/tlswrapper)](https://goreportcard.com/report/github.com/hexian000/tlswrapper)
[![Downloads](https://img.shields.io/github/downloads/hexian000/tlswrapper/total.svg)](https://github.com/hexian000/tlswrapper/releases)
[![Release](https://img.shields.io/github/release/hexian000/tlswrapper.svg?style=flat)](https://github.com/hexian000/tlswrapper/releases)

Wrap any TCP-based service with multiplexed mutual TLS tunnels.

Status: **Stable**

- [Features](#features)
- [Protocol Stack](#protocol-stack)
- [Authentication Model](#authentication-model)
- [Quick Start](#quick-start)
  - [Generating Key Pair](#generating-key-pair)
  - [Creating Config Files (Forward Case)](#creating-config-files-forward-case)
    - [server.json](#serverjson)
    - [client.json](#clientjson)
  - [Creating Config Files (Reverse Case)](#creating-config-files-reverse-case)
    - [server.json](#serverjson-1)
    - [client.json](#clientjson-1)
    - [Options](#options)
  - [Start](#start)
- [Building from Source](#building-from-source)
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

Like SSH, each peer should have a key pair (X.509 certificate + private key) and an authorized list. Only certificates in the authorized list can communicate with the peer.

This behavior is based on TLS 1.3 implemented by "crypto/tls" library in Go.

By default, all certificates are self-signed. This will not reduce security. 

## Quick Start

### Generating Key Pair

```sh
./tlswrapper -gencerts client,server
```

Now we will have `client-cert.pem`, `client-key.pem`, `server-cert.pem` and `server-key.pem`.

### Creating Config Files (Forward Case)

#### server.json

```json
{
  "muxlisten": "0.0.0.0:38000",
  "services": {
    "http": "127.0.0.1:8080"
  },
  "certs": [
    {
      "cert": "server-cert.pem",
      "key": "server-key.pem"
    }
  ],
  "authcerts": [
    {
      "cert": "client-cert.pem"
    }
  ]
}
```

#### client.json

```json
{
  "peers": {
    "server": {
      "addr": "example.com:38000",
      "listen": "127.0.0.1:8080",
      "peerservice": "http"
    }
  },
  "certs": [
    {
      "cert": "client-cert.pem",
      "key": "client-key.pem"
    }
  ],
  "authcerts": [
    {
      "cert": "server-cert.pem"
    }
  ]
}
```

### Creating Config Files (Reverse Case)

#### server.json

```json
{
  "muxlisten": "0.0.0.0:38000",
  "peers": {
    "client": {
      "listen": "127.0.0.1:8080",
      "peerservice": "http"
    }
  },
  "certs": [
    {
      "cert": "server-cert.pem",
      "key": "server-key.pem"
    }
  ],
  "authcerts": [
    {
      "cert": "client-cert.pem"
    }
  ]
}
```

#### client.json

```json
{
  "peername": "client",
  "services": {
    "http": "127.0.0.1:8080"
  },
  "peers": {
    "server": {
      "addr": "example.com:38000"
    }
  },
  "certs": [
    {
      "cert": "client-cert.pem",
      "key": "client-key.pem"
    }
  ],
  "authcerts": [
    {
      "cert": "server-cert.pem"
    }
  ]
}
```

#### Options

- "peername": local peer name
- "muxlisten": listener bind address
- "services": local service forwards
- "services[\*]": local service dial address
- "peers": named peers to that need to keep connected
- "peers[\*].addr": dial address
- "peers[\*].listen": listen for port forwarding
- "peers[\*].peerservice": the service name we ask the peer for
- "certs": local certificates
- "certs[\*].cert": certificate PEM file path
- "certs[\*].key": private key PEM file path
- "authcerts": peer authorized certificates list, bundles are supported
- "authcerts[\*].cert": certificate PEM file path

See [source code](v3/config.go) for a complete list of all available options.

See [config.json](config.json) for example config file.

### Start

```sh
./tlswrapper -c server.json

./tlswrapper -c client.json
```

## Building from Source

```sh
# get source code
git clone https://github.com/hexian000/tlswrapper.git
cd tlswrapper
git checkout <some version>
# build an executable for local system
./make.sh r
```

## Credits

- [go](https://github.com/golang/go)
- [gosnippets](https://github.com/hexian000/gosnippets)
- [yamux](https://github.com/hashicorp/yamux)
