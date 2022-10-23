package session

import (
	"crypto/tls"

	"github.com/xtaci/smux"
)

type Config struct {
	TLS *tls.Config
	Mux *smux.Config
}
