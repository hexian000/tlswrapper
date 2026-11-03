// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package mux

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDatagramQueues(t *testing.T) {
	var disabled *DatagramChannel
	if disabled.Ready() || !errors.Is(disabled.TrySend(1, nil), ErrDatagramsUnavailable) {
		t.Fatal("disabled extension became ready")
	}
	if NewDatagramChannel(false, true, 1, nil) != nil {
		t.Fatal("unnegotiated channel")
	}
	m := &SessionMetrics{}
	d := NewDatagramChannel(true, true, 2, m)
	t.Cleanup(d.Close)
	if err := d.TrySend(1, nil); !errors.Is(err, ErrDatagramsUnavailable) {
		t.Fatal(err)
	}
	d.Activate()
	if d.LocalParity() != 1 {
		t.Fatal("client parity")
	}
	if err := d.TrySend(0, nil); !errors.Is(err, ErrInvalidDatagram) {
		t.Fatal(err)
	}
	if err := d.TrySend(1, make([]byte, 65536)); !errors.Is(err, ErrDatagramTooLarge) {
		t.Fatal(err)
	}
	p := []byte("owned")
	if err := d.TrySend(1, p); err != nil {
		t.Fatal(err)
	}
	p[0] = 'X'
	if err := d.TryClose(1); err != nil {
		t.Fatal(err)
	}
	if err := d.TrySend(3, nil); !errors.Is(err, ErrDatagramQueueFull) {
		t.Fatal(err)
	}
	d.SetQueueLimit(1)
	v, err := d.next(context.Background(), true)
	if err != nil || string(v.Data) != "owned" {
		t.Fatalf("copy: %+v, %v", v, err)
	}
	d.Deliver(Datagram{FlowID: 2})
	d.Deliver(Datagram{FlowID: 0})
	d.Deliver(Datagram{FlowID: 2, Close: true, Data: []byte("invalid")})
	d.Deliver(Datagram{FlowID: 4, Data: []byte("full")})
	v, err = d.Receive(context.Background())
	if err != nil || v.FlowID != 2 || len(v.Data) != 0 || v.Close {
		t.Fatalf("empty packet: %+v, %v", v, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = d.Receive(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if m.DatagramsDropped.Load() != 5 || m.DatagramsReceived.Load() != 2 {
		t.Fatalf("metrics: %+v", m.DatagramStats())
	}
	d.Close()
	if d.Ready() {
		t.Fatal("closed channel ready")
	}
	if err = d.TrySend(1, nil); !errors.Is(err, ErrSessionClosed) {
		t.Fatal(err)
	}
}

func TestDatagramBlockedSenderIsBounded(t *testing.T) {
	d := NewDatagramChannel(true, false, 2, nil)
	d.Activate()
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	if err := d.TrySend(2, nil); err != nil {
		t.Fatal(err)
	}
	go func() {
		done <- d.SendLoop(context.Background(), func(Datagram) error { close(started); <-release; return ErrSessionClosed })
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("sender did not start")
	}
	for i := 0; i < 10000; i++ {
		_ = d.TrySend(2, []byte("congested"))
	}
	d.mu.Lock()
	n := len(d.send)
	d.mu.Unlock()
	if n != 2 {
		t.Fatalf("queue grew to %d", n)
	}
	d.Close()
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("sender leaked")
	}
	d.Wait()
}

func TestDatagramSenderShedsOversize(t *testing.T) {
	m := &SessionMetrics{}
	d := NewDatagramChannel(true, true, 2, m)
	d.Activate()
	_ = d.TrySend(1, []byte("too large"))
	_ = d.TrySend(1, nil)
	done, sent := make(chan error, 1), make(chan struct{})
	go func() {
		done <- d.SendLoop(context.Background(), func(v Datagram) error {
			if len(v.Data) > 0 {
				return ErrDatagramTooLarge
			}
			close(sent)
			return nil
		})
	}()
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("oversized datagram stalled sender")
	}
	d.Close()
	<-done
	if m.DatagramsSent.Load() != 1 || m.DatagramsDropped.Load() != 1 {
		t.Fatalf("metrics: %+v", m.DatagramStats())
	}
}
