// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package mux

import (
	"context"
	"errors"
	"sync"
	"time"
)

var (
	ErrDatagramsUnavailable = errors.New("lossymux not ready or negotiated")
	ErrDatagramQueueFull    = errors.New("lossymux queue full")
	ErrDatagramTooLarge     = errors.New("lossymux datagram too large")
	ErrInvalidDatagram      = errors.New("invalid lossymux datagram")
)

// DatagramSession is optional. A nil channel means the bilateral extension was
// not negotiated (or an inbound session has not completed its handshake).
type DatagramSession interface {
	Datagrams() *DatagramChannel
}

// Datagram distinguishes a zero-length UDP payload from a flow-close message.
// Flow IDs are a separate namespace: client odd, server even, zero reserved.
type Datagram struct {
	FlowID uint16
	Data   []byte
	Close  bool
}

// DatagramChannel owns bounded application queues. Exactly one transport sender
// runs SendLoop; exactly one relay consumer calls Receive. Submission never waits
// for transport I/O. Lower-level transport buffering is additional to these queues.
type DatagramChannel struct {
	mu                 sync.Mutex
	send, recv         []Datagram
	limit              int
	ready, closed      bool
	parity             uint16
	sendWake, recvWake chan struct{}
	done               chan struct{}
	senderDone         chan struct{}
	metrics            *SessionMetrics
}

func NewDatagramChannel(negotiated, client bool, limit int, metrics *SessionMetrics) *DatagramChannel {
	if !negotiated {
		return nil
	}
	if metrics == nil {
		metrics = &SessionMetrics{}
	}
	if limit < 1 || limit > 65535 {
		limit = 64
	}
	d := &DatagramChannel{limit: limit, metrics: metrics, done: make(chan struct{}),
		sendWake: make(chan struct{}, 1), recvWake: make(chan struct{}, 1)}
	if client {
		d.parity = 1
	}
	return d
}

// LocalParity returns the parity used for locally allocated flow IDs.
func (d *DatagramChannel) LocalParity() uint16 { return d.parity }

func (d *DatagramChannel) Ready() bool {
	if d == nil {
		return false
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.ready && !d.closed
}

// Activate is called only after the negotiated transport channel is attached.
func (d *DatagramChannel) Activate() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.closed {
		d.ready = true
	}
}

func notifyDatagram(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

func (d *DatagramChannel) TrySend(flow uint16, data []byte) error {
	return d.submit(Datagram{FlowID: flow, Data: data})
}

func (d *DatagramChannel) TryClose(flow uint16) error {
	return d.submit(Datagram{FlowID: flow, Close: true})
}

func (d *DatagramChannel) submit(v Datagram) error {
	if d == nil {
		return ErrDatagramsUnavailable
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var err error
	switch {
	case v.FlowID == 0:
		err = ErrInvalidDatagram
	case len(v.Data) > 65535:
		err = ErrDatagramTooLarge
	case d.closed:
		err = ErrSessionClosed
	case !d.ready:
		err = ErrDatagramsUnavailable
	case len(d.send) >= d.limit:
		err = ErrDatagramQueueFull
	}
	if err != nil {
		if !v.Close && !errors.Is(err, ErrSessionClosed) {
			d.metrics.DatagramsDropped.Add(1)
		}
		return err
	}
	v.Data = append([]byte(nil), v.Data...)
	d.send = append(d.send, v)
	if !v.Close {
		d.metrics.LastDatagram.Store(time.Now().UnixNano())
	}
	notifyDatagram(d.sendWake)
	return nil
}

// Deliver accepts an owned transport message. Invalid extension messages are
// ignored without affecting TCP. A full receive queue sheds the incoming packet.
func (d *DatagramChannel) Deliver(v Datagram) {
	if v.FlowID == 0 || len(v.Data) > 65535 || (v.Close && len(v.Data) != 0) {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed || !d.ready {
		return
	}
	if !v.Close {
		d.metrics.DatagramsReceived.Add(1)
		d.metrics.DatagramBytesReceived.Add(uint64(len(v.Data)))
		d.metrics.LastDatagram.Store(time.Now().UnixNano())
	}
	if len(d.recv) >= d.limit {
		if !v.Close {
			d.metrics.DatagramsDropped.Add(1)
		}
		return
	}
	d.recv = append(d.recv, v)
	notifyDatagram(d.recvWake)
}

func (d *DatagramChannel) next(ctx context.Context, sending bool) (Datagram, error) {
	for {
		if err := ctx.Err(); err != nil {
			return Datagram{}, err
		}
		d.mu.Lock()
		q, wake := &d.recv, d.recvWake
		if sending {
			q, wake = &d.send, d.sendWake
		}
		if d.closed {
			d.mu.Unlock()
			return Datagram{}, ErrSessionClosed
		}
		if len(*q) > 0 {
			v := (*q)[0]
			(*q)[0] = Datagram{}
			*q = (*q)[1:]
			d.mu.Unlock()
			return v, nil
		}
		d.mu.Unlock()
		select {
		case <-ctx.Done():
			return Datagram{}, ctx.Err()
		case <-d.done:
			return Datagram{}, ErrSessionClosed
		case <-wake:
		}
	}
}

func (d *DatagramChannel) Receive(ctx context.Context) (Datagram, error) {
	return d.next(ctx, false)
}

// SendLoop handles the only transport writer. Too-large errors shed one packet;
// other transport failures terminate the loop so the session can recover.
func (d *DatagramChannel) SendLoop(ctx context.Context, send func(Datagram) error) error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return ErrSessionClosed
	}
	d.senderDone = make(chan struct{})
	done := d.senderDone
	d.mu.Unlock()
	defer close(done)
	for {
		v, err := d.next(ctx, true)
		if err != nil {
			return err
		}
		if err = send(v); err != nil {
			if !v.Close {
				d.metrics.DatagramsDropped.Add(1)
			}
			if errors.Is(err, ErrDatagramTooLarge) {
				continue
			}
			return err
		}
		if !v.Close {
			d.metrics.DatagramsSent.Add(1)
			d.metrics.DatagramBytesSent.Add(uint64(len(v.Data)))
		}
	}
}

// Wait is used after closing the transport, before folding its final metrics.
func (d *DatagramChannel) Wait() {
	if d == nil {
		return
	}
	d.mu.Lock()
	done := d.senderDone
	d.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (d *DatagramChannel) discard(q []Datagram) {
	for _, v := range q {
		if !v.Close {
			d.metrics.DatagramsDropped.Add(1)
		}
	}
}

// SetQueueLimit applies live. Shrinking retains the oldest queued messages.
func (d *DatagramChannel) SetQueueLimit(limit int) {
	if d == nil || limit < 1 || limit > 65535 {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.limit = limit
	for _, q := range []*[]Datagram{&d.send, &d.recv} {
		if len(*q) > limit {
			d.discard((*q)[limit:])
			*q = append([]Datagram(nil), (*q)[:limit]...)
		}
	}
}

func (d *DatagramChannel) Close() {
	if d == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed {
		return
	}
	d.closed = true
	d.discard(d.send)
	d.discard(d.recv)
	d.send, d.recv = nil, nil
	close(d.done)
}
