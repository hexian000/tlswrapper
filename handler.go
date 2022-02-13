package main

import (
	"context"
	"net"
)

type Handler interface {
	Serve(context.Context, net.Conn)
}
