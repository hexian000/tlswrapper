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
		w, err := syslog.New(syslog.LOG_USER|syslog.LOG_NOTICE, tag)
		if err != nil {
			return nil, err
		}
		return &syslogOutput{
			buf: make([]byte, 0),
			out: w,
		}, nil
	}
}

var priorityMap = [...]func(*syslog.Writer, string) error{
	(*syslog.Writer).Alert,
	(*syslog.Writer).Crit,
	(*syslog.Writer).Err,
	(*syslog.Writer).Warning,
	(*syslog.Writer).Notice,
	(*syslog.Writer).Info,
	(*syslog.Writer).Debug,
	(*syslog.Writer).Debug,
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
	s.buf = buf
	_ = priorityMap[m.level](s.out, string(buf))
}
