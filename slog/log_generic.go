//go:build !linux

package slog

import (
	"net"
	"net/url"
	"os"
	"strings"
)

func (l *Logger) ParseOutput(output, tag string) error {
	switch true {
	case output == "" || strings.EqualFold(output, "stderr"):
		l.SetOutput(os.Stderr)
		return nil
	case strings.EqualFold(output, "stdout"):
		l.SetOutput(os.Stdout)
		return nil
	case strings.EqualFold(output, "syslog"):
		return errors.New("syslog is not supported on current platform")
	}
	// otherwise, the string must be a url
	u, err := url.Parse(output)
	if err != nil {
		return err
	}
	conn, err := net.Dial(u.Scheme, u.Host)
	if err != nil {
		return err
	}
	l.SetOutput(conn)
	return nil
}
