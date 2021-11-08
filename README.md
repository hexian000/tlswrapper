# tlswrapper

[![Go Report Card](https://goreportcard.com/badge/github.com/hexian000/tlswrapper)](https://goreportcard.com/report/github.com/hexian000/tlswrapper)

## What for

Connect multiple sites across the Internet with a multiplexing mTLS tunnel, safely. 

```
       Trusted      |     Untrusted      |     Trusted
                                  +------------+    +-------+
                             +-1->| tlswrapper |-n->| Peer2 |
                             |    +------------+    +-------+
+-------+    +------------+  |    +------------+    +-------+
| Peer1 |-n->| tlswrapper |--+-1->| tlswrapper |-n->| Peer3 |
+-------+    +------------+  |    +------------+    +-------+
                             |    +------------+    +-------+
                             +-1->| tlswrapper |-n->| Peer4 |
                                  +------------+    +-------+
```

## Protocol Stack

```
+-------------------------------+
| HTTP CONNECT Proxy (optional) |
+-------------------------------+
|   yamux stream multiplexing   |
+-------------------------------+
|        mutual TLS 1.3         |
+-------------------------------+
|  TCP/IP (untrusted network)   |
+-------------------------------+
```


## Authentication Model

Like SSH, each peer needs to generate a key pair(certificate + private key). Only certificate in a peer's authorized certificates list can communicate with this peer.

This behavior is based on golang's mutual TLS 1.3 implementation.

By default, all certificates are self-signed. This will not reduce security. 

## Quick Start

### 1. Generate key pair (or use your own):

```sh
./gencerts.sh peer1 peer2
```

### 2. Create "config.json" per peer

#### Peer1

```json
{
  "server": [
    {
      "listen": "0.0.0.0:12345"
    }
  ],
  "client": [
    {
      "listen": "127.0.0.1:8080",
      "dial": "peer2.example.com:12345"
    }
  ],
  "cert": "peer1-cert.pem",
  "key": "peer1-key.pem",
  "authcerts": [
    "peer2-cert.pem"
  ]
}
```

#### Peer2

```json
{
  "server": [
    {
      "listen": "0.0.0.0:12345"
    }
  ],
  "client": [
    {
      "listen": "127.0.0.1:8080",
      "dial": "peer1.example.com:12345",
      "proxy": [
        {
          "listen": ":5201",
          "forward": "gateway.peer1.lan:5201"
        }
      ]
    }
  ],
  "cert": "peer2-cert.pem",
  "key": "peer2-key.pem",
  "authcerts": [
    "peer1-cert.pem"
  ]
}
```

#### Options

- "server": TLS listener configs
- "server[\*].listen": server bind address
- "server[\*].forward": (optional) upstream TCP service address, leave empty or unconfigured to use builtin HTTP proxy
- "client": TLS client configs
- "client[\*].listen": proxy listen address
- "client[\*].dial": server address
- "client[\*].proxy": (optional) proxy forwarder configs
- "client[\*].proxy[\*].listen": forwarder listen address
- "client[\*].proxy[\*].forward": forwarder destination address
- "cert": peer certificate
- "key": peer private key
- "authcerts": server authorized certificates list, bundles are supported


### 3. Start

```sh
./tlswrapper -c config.json
```

You may found the systemd user unit [tlswrapper.service](tlswrapper.service) is useful.

## Build/Install

```sh
git clone https://github.com/hexian000/tlswrapper.git
cd tlswrapper
./make.sh
```
or

```sh
go install github.com/hexian000/tlswrapper
```

## Credits

- [go](https://github.com/golang/go)
- [yamux](https://github.com/hashicorp/yamux)
