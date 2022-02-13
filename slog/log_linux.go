//go:build linux

package slog

import (
	"fmt"
	"log/syslog"
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
		w, err := syslog.New(syslog.LOG_NOTICE, fmt.Sprintf("%s [%v]", tag, os.Getpid()))
		if err != nil {
			return err
		}
		l.SetOutput(w)
		return nil
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
