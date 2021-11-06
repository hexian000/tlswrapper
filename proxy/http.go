package proxy

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"net/url"
	"sync"
)

type Conn struct {
	net.Conn
	rd *bufio.Reader

	isClient       bool
	handshakeDone  bool
	handshakeFn    func() error
	handshakeMutex sync.Mutex
	host           string
}

func Client(conn net.Conn, host string) *Conn {
	c := &Conn{
		Conn:     conn,
		rd:       bufio.NewReader(conn),
		host:     host,
		isClient: true,
	}
	c.handshakeFn = c.clientHandshake
	return c
}

func Server(conn net.Conn) *Conn {
	c := &Conn{
		Conn: conn,
		rd:   bufio.NewReader(conn),
	}
	c.handshakeFn = c.serverHandshake
	return c
}

func (c *Conn) Read(p []byte) (n int, err error) {
	if err := c.Handshake(); err != nil {
		return 0, err
	}
	return c.rd.Read(p)
}

func (c *Conn) Write(b []byte) (n int, err error) {
	if err := c.Handshake(); err != nil {
		return 0, err
	}
	return c.Conn.Write(b)
}

func (c *Conn) Host() string {
	return c.host
}

func (c *Conn) clientHandshake() error {
	req := &http.Request{
		Method:     http.MethodConnect,
		URL:        &url.URL{Host: c.host},
		Host:       c.host,
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
	}
	req.Header.Set("Proxy-Connection", "keep-alive")
	if err := req.Write(c.Conn); err != nil {
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

func (c *Conn) serverHandshake() error {
	req, err := http.ReadRequest(c.rd)
	if err != nil {
		return err
	}
	if req.Method != http.MethodConnect {
		resp := &http.Response{
			StatusCode: http.StatusMethodNotAllowed,
			ProtoMajor: 1,
			ProtoMinor: 1,
			Header:     make(http.Header),
		}
		_ = resp.Write(c.Conn)
		return http.ErrNotSupported
	}
	c.host = req.Host
	resp := &http.Response{
		StatusCode: http.StatusOK,
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
	}
	err = resp.Write(c.Conn)
	if err != nil {
		return err
	}
	c.handshakeDone = true
	return nil
}

func (c *Conn) Handshake() error {
	c.handshakeMutex.Lock()
	defer c.handshakeMutex.Unlock()
	if c.handshakeDone {
		return nil
	}
	return c.handshakeFn()
}
