//go:build linux

package slog

import (
	"fmt"
	"log/syslog"
	"net"
	"net/url"
	"os"
	"path"
	"runtime"
	"strconv"
	"strings"
	"time"
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

func (l *Logger) Output(calldepth int, level int, s string) {
	now := time.Now()
	if func() bool {
		l.mu.Lock()
		defer l.mu.Unlock()
		return level < l.level
	}() {
		return
	}
	_, file, line, ok := runtime.Caller(calldepth)
	if !ok {
		file, line = "???", 0
	} else {
		file = path.Base(file)
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	buf := l.buf[:0]
	buf = append(buf, levelChar[level], ' ')
	if _, ok := l.out.(*syslog.Writer); !ok {
		buf = now.AppendFormat(buf, ISO8601Milli)
		buf = append(buf, ' ')
	}
	buf = append(buf, file...)
	buf = append(buf, ':')
	buf = strconv.AppendInt(buf, int64(line), 10)
	buf = append(buf, ' ')
	buf = append(buf, s...)
	if len(s) == 0 || s[len(s)-1] != '\n' {
		buf = append(buf, '\n')
	}
	l.buf = buf
	l.out.Write(buf)
}
