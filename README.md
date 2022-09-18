# tlswrapper

[![Go Report Card](https://goreportcard.com/badge/github.com/hexian000/tlswrapper)](https://goreportcard.com/report/github.com/hexian000/tlswrapper)

## What for

Wrap your TCP-based service with multiplexing mTLS tunnels. 

If you are also interested in building a reverse proxy cluster and/or accessing any service published by any peer, see also [gated](https://github.com/hexian000/gated).

```
       Trusted      |     Untrusted    |     Trusted
+--------+    +------------+    +------------+    +--------+
| Client |-n->| tlswrapper |-1->| tlswrapper |-n->| Server |
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
    "server": {
        "listen": ":8443",
        "cert": "server-cert.pem",
        "key": "server-key.pem",
        "authcerts": [
            "client-cert.pem"
        ]
    },
    "local": {
        "forward": "127.0.0.1:80"
    }
}
```

#### Client

```json
{
    "server": {
        "dial": [
            "site1.example.com:8443",
            "site2.example.com:8443"
        ],
        "cert": "server-cert.pem",
        "key": "server-key.pem",
        "authcerts": [
            "client-cert.pem"
        ]
    },
    "local": {
        "listen": "127.0.0.1:8080"
    }
}
```

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
# build for native system
./make.sh
```
or
```sh
go install github.com/hexian000/tlswrapper
```

## Credits

- [go](https://github.com/golang/go)
- [yamux](https://github.com/hashicorp/yamux)
