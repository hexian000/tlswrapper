# tlswrapper

## What for

This proxy transmits any TCP services through multiplexed mTLS 1.3 tunnels.

## Authenticate Model

Like SSH, each peer needs to generate a key pair(certificate + private key). Only certificate in a peer's authorized certificates list can communicate with this peer.

This behavior is based on golang's mutual TLS 1.3 implementation.

By default, all certificates are self-signed. This will not reduce security. 

## Quick Start

### Generate key pair (or use your own):

```sh
./gencerts.sh peer1 peer2
```

### Create "config.json" per peer

```json
{
  "server": [
    {
      "listen": "0.0.0.0:52010",
      "forward": "127.0.0.1:8118"
    }
  ],
  "client": [
    {
      "listen": "127.0.0.1:8119",
      "dial": "example.com:52010"
    }
  ],
  "cert": "peer1-cert.pem",
  "key": "peer1-key.pem",
  "authcerts": [
    "peer2-cert.pem"
  ]
}
```

- "server": Listen for tunnel clients and forward to any TCP service
- "client": Listen for TCP and forward through tunnel
- "cert": Local certificate.
- "key": Local private key.
- "authcerts": Local authorized certificates list.


### Start

```sh
./tlswrapper -c config.json
```

## Build

```sh
git clone https://github.com/hexian000/tlswrapper.git
cd tlswrapper
go mod vendor
./make.sh
```

## Credits

- [go](https://github.com/golang/go)
- [yamux](https://github.com/hashicorp/yamux)
