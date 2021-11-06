package proxy

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"net/url"
)

type Conn struct {
	net.Conn
	rd *bufio.Reader
}

func (c *Conn) Read(p []byte) (n int, err error) {
	return c.rd.Read(p)
}

func Client(conn net.Conn, address string) (net.Conn, error) {
	req := &http.Request{
		Method:     http.MethodConnect,
		URL:        &url.URL{Host: address},
		Host:       address,
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
	}
	req.Header.Set("Proxy-Connection", "keep-alive")

	if err := req.Write(conn); err != nil {
		return nil, err
	}

	rd := bufio.NewReader(conn)
	resp, err := http.ReadResponse(rd, req)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, errors.New(resp.Status)
	}

	return &Conn{conn, rd}, nil
}
