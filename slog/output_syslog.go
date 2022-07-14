//go:build !windows && !plan9
// +build !windows,!plan9

package slog

import (
	"log/syslog"
	"strconv"
	"sync"
)

type syslogOutput struct {
	mu  sync.Mutex
	buf []byte
	out *syslog.Writer
}

func init() {
	builtinOutput["syslog"] = func(tag string) (logOutput, error) {
		w, err := syslog.New(syslog.LOG_DAEMON|syslog.LOG_NOTICE, tag)
		if err != nil {
			return nil, err
		}
		return &syslogOutput{
			buf: make([]byte, 0),
			out: w,
		}, nil
	}
}

func (s *syslogOutput) Write(m logMessage) {
	s.mu.Lock()
	defer s.mu.Unlock()
	buf := s.buf[:0]
	buf = append(buf, levelChar[m.level], ' ')
	buf = append(buf, m.file...)
	buf = append(buf, ':')
	buf = strconv.AppendInt(buf, int64(m.line), 10)
	buf = append(buf, ' ')
	buf = append(buf, m.msg...)
	if len(m.msg) == 0 || m.msg[len(m.msg)-1] != '\n' {
		buf = append(buf, '\n')
	}
	s.buf = buf
	s.out.Write(buf)
}
