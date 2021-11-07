package proxy

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

type ClientConn struct {
	net.Conn
	rd *bufio.Reader

	handshakeDone  bool
	handshakeMutex sync.Mutex
	host           string
}

func Client(conn net.Conn, host string) *ClientConn {
	return &ClientConn{
		Conn: conn,
		rd:   bufio.NewReader(conn),
		host: host,
	}
}

func (c *ClientConn) Read(p []byte) (n int, err error) {
	if err := c.Handshake(); err != nil {
		return 0, err
	}
	return c.rd.Read(p)
}

func (c *ClientConn) Write(b []byte) (n int, err error) {
	if err := c.Handshake(); err != nil {
		return 0, err
	}
	return c.Conn.Write(b)
}

func (c *ClientConn) clientHandshake(ctx context.Context) error {
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(deadline)
		defer func() {
			_ = c.SetDeadline(time.Time{})
		}()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodConnect, c.host, nil)
	if err != nil {
		return err
	}
	if err := req.WriteProxy(c.Conn); err != nil {
		return err
	}
	resp, err := http.ReadResponse(c.rd, req)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return errors.New(resp.Status)
	}
	c.handshakeDone = true
	return nil
}

func (c *ClientConn) Handshake() error {
	return c.HandshakeContext(context.Background())
}

func (c *ClientConn) HandshakeContext(ctx context.Context) (ret error) {
	handshakeCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	if ctx.Done() != nil {
		done := make(chan struct{})
		interruptRes := make(chan error, 1)
		defer func() {
			close(done)
			if ctxErr := <-interruptRes; ctxErr != nil {
				ret = ctxErr
			}
		}()
		go func() {
			select {
			case <-handshakeCtx.Done():
				_ = c.Conn.Close()
				interruptRes <- handshakeCtx.Err()
			case <-done:
				interruptRes <- nil
			}
		}()
	}

	c.handshakeMutex.Lock()
	defer c.handshakeMutex.Unlock()
	if c.handshakeDone {
		return nil
	}
	return c.clientHandshake(handshakeCtx)
}

type Handler struct {
	Connect http.Handler
	Default http.Handler
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodConnect {
		h.Connect.ServeHTTP(w, req)
		return
	}
	h.Default.ServeHTTP(w, req)
}

type HijackedConn struct {
	net.Conn
	rd io.Reader
}

func (c *HijackedConn) Read(p []byte) (n int, err error) {
	return c.rd.Read(p)
}

func Hijack(w http.ResponseWriter) (net.Conn, error) {
	h, ok := w.(http.Hijacker)
	if !ok {
		return nil, errors.New("hijacking is not supported")
	}
	conn, rw, err := h.Hijack()
	if err != nil {
		return nil, err
	}
	err = rw.Flush()
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	return &HijackedConn{conn, rw}, nil
}
