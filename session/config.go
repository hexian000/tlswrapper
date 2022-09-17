package session

import (
	"crypto/tls"

	"github.com/hashicorp/yamux"
)

type Config struct {
	TLS *tls.Config
	Mux *yamux.Config
}
