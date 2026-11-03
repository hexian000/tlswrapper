// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h3mux_test

import (
	"bytes"
	"context"
	"fmt"
	"github.com/quic-go/quic-go"
	"testing"
	"time"

	"github.com/hexian000/tlswrapper/v4/mux"
	. "github.com/hexian000/tlswrapper/v4/mux/h3mux"
)

func TestLossyMuxNegotiationAndDatagrams(t *testing.T) {
	for _, offers := range [][2]bool{{false, false}, {true, false}, {false, true}, {true, true}} {
		t.Run(fmt.Sprint(offers), func(t *testing.T) {
			cli, srv := quicSessions(t, &Config{LossyMux: offers[0]}, &Config{LossyMux: offers[1]})
			cd, sd := cli.(mux.DatagramSession).Datagrams(), srv.(mux.DatagramSession).Datagrams()
			if !offers[0] || !offers[1] {
				if cd != nil || sd != nil {
					t.Fatal("extension was not bilateral")
				}
				conn, err := cli.Open(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				peer, err := srv.Accept()
				if err != nil {
					t.Fatal(err)
				}
				defer peer.Close()
				transferAndVerify(t, conn, peer, []byte("legacy TCP"))
				return
			}
			if !cd.Ready() || !sd.Ready() || cd.LocalParity() != 1 || sd.LocalParity() != 0 {
				t.Fatal("channel not ready")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			for _, direction := range []struct {
				src, dst *mux.DatagramChannel
				id       uint16
			}{{cd, sd, 1}, {sd, cd, 2}} {
				for _, payload := range [][]byte{nil, []byte("packet")} {
					if err := direction.src.TrySend(direction.id, payload); err != nil {
						t.Fatal(err)
					}
					v, err := direction.dst.Receive(ctx)
					if err != nil || v.Close || v.FlowID != direction.id || !bytes.Equal(v.Data, payload) {
						t.Fatalf("datagram %+v: %v", v, err)
					}
				}
				_ = direction.src.TryClose(direction.id)
				v, err := direction.dst.Receive(ctx)
				if err != nil || !v.Close || v.FlowID != direction.id {
					t.Fatalf("close %+v: %v", v, err)
				}
			}
			if err := cd.TrySend(1, make([]byte, 65535)); err != nil {
				t.Fatal(err)
			}
			_ = cd.TrySend(1, []byte("after oversize"))
			v, err := sd.Receive(ctx)
			if err != nil || string(v.Data) != "after oversize" {
				t.Fatalf("oversize recovery: %+v %v", v, err)
			}
			if cli.Stats().DatagramsDropped.Load() != 1 || cli.IsClosed() {
				t.Fatal("oversize handling")
			}
		})
	}
}

func TestLossyMuxRequiresNativeDatagramSupport(t *testing.T) {
	serverTLS, clientTLS := generateSelfSignedTLS(t)
	serverTLS.NextProtos = []string{"h3"}
	l, err := quic.ListenAddr("127.0.0.1:0", serverTLS, &quic.Config{EnableDatagrams: false})
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	type result struct {
		ss  mux.Session
		err error
	}
	ready := make(chan result, 1)
	go func() {
		conn, err := l.Accept(ctx)
		if err != nil {
			ready <- result{err: err}
			return
		}
		ss, err := NewSession(ctx, conn, &Config{LossyMux: true, TLSConfig: serverTLS})
		ready <- result{ss, err}
	}()
	cli, err := Dial(ctx, l.Addr().String(), &Config{LossyMux: true, TLSConfig: clientTLS})
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	var srv mux.Session
	select {
	case res := <-ready:
		if res.err != nil {
			t.Fatal(res.err)
		}
		srv = res.ss
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	defer srv.Close()
	if cli.(mux.DatagramSession).Datagrams() != nil || srv.(mux.DatagramSession).Datagrams() != nil {
		t.Fatal("activated without QUIC DATAGRAM support")
	}
	conn, err := cli.Open(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	peer, err := srv.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer peer.Close()
	transferAndVerify(t, conn, peer, []byte("TCP survives unsupported optional transport"))
}
