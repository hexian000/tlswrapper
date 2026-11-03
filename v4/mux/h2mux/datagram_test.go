// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h2mux

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/hexian000/tlswrapper/v4/mux"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestLossyMuxNegotiationAndDatagrams(t *testing.T) {
	for _, offers := range [][2]bool{{false, false}, {true, false}, {false, true}, {true, true}} {
		t.Run(fmt.Sprint(offers), func(t *testing.T) {
			cli, srv := pipeSession(t, &Config{LossyMux: offers[0]}, &Config{LossyMux: offers[1]})
			cd, sd := cli.(mux.DatagramSession).Datagrams(), srv.(mux.DatagramSession).Datagrams()
			if !offers[0] || !offers[1] {
				if cd != nil || sd != nil {
					t.Fatal("extension was not bilateral")
				}
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				unexpected, err := cli.(*clientSession).grpcClient.Datagrams(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := unexpected.Recv(); status.Code(err) != codes.FailedPrecondition {
					t.Fatalf("unnegotiated RPC: %v", err)
				}
				if cli.IsClosed() || srv.IsClosed() {
					t.Fatal("unnegotiated RPC killed TCP session")
				}
				stream, err := cli.Open(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				defer stream.Close()
				peer, err := srv.Accept()
				if err != nil {
					t.Fatal(err)
				}
				defer peer.Close()
				transferAndVerify(t, stream, peer, []byte("legacy TCP"))
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
				for _, payload := range [][]byte{nil, []byte("packet"), bytes.Repeat([]byte("z"), 65535)} {
					if err := direction.src.TrySend(direction.id, payload); err != nil {
						t.Fatal(err)
					}
					v, err := direction.dst.Receive(ctx)
					if err != nil || v.Close || v.FlowID != direction.id || !bytes.Equal(v.Data, payload) {
						t.Fatalf("datagram %+v: %v", v, err)
					}
				}
				if err := direction.src.TryClose(direction.id); err != nil {
					t.Fatal(err)
				}
				v, err := direction.dst.Receive(ctx)
				if err != nil || !v.Close || v.FlowID != direction.id {
					t.Fatalf("close %+v: %v", v, err)
				}
			}
			if cli.Stats().NumStreams.Load() != 0 || srv.Stats().NumStreams.Load() != 0 {
				t.Fatal("datagram RPC counted as application stream")
			}
			stream, err := cli.(*clientSession).grpcClient.Datagrams(ctx)
			if err != nil {
				t.Fatal(err)
			}
			_, err = stream.Recv()
			if status.Code(err) != codes.AlreadyExists {
				t.Fatalf("duplicate: %v", err)
			}
			if cli.IsClosed() || srv.IsClosed() {
				t.Fatal("duplicate channel killed session")
			}
		})
	}
}

func TestLossyMuxCongestionDoesNotBlockTCP(t *testing.T) {
	cli, srv := pipeSession(t, &Config{LossyMux: true, DatagramQueue: 1}, &Config{LossyMux: true, DatagramQueue: 1})
	d := cli.(mux.DatagramSession).Datagrams()
	for i := 0; i < 1000; i++ {
		_ = d.TrySend(1, []byte("overflow"))
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
	transferAndVerify(t, conn, peer, []byte("TCP still works"))
	if cli.Stats().DatagramsDropped.Load()+srv.Stats().DatagramsDropped.Load() == 0 {
		t.Fatal("congestion did not shed packets")
	}
}
