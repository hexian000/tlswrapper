package slog

import (
	"io"
	"os"
	"strconv"
	"sync"
	"time"
)

type logMessage struct {
	timestamp time.Time
	level     int
	file      []byte
	line      int
	msg       []byte
}

type logOutput interface {
	Write(m logMessage)
}

var builtinOutput map[string]func(tag string) (logOutput, error)

func init() {
	builtinOutput = map[string]func(tag string) (logOutput, error){
		"discard": func(string) (logOutput, error) {
			return nil, nil
		},
		"stdout": func(string) (logOutput, error) {
			return newLineOutput(os.Stdout), nil
		},
		"stderr": func(string) (logOutput, error) {
			return newLineOutput(os.Stderr), nil
		},
	}
}

type lineOutput struct {
	mu  sync.Mutex
	buf []byte
	out io.Writer
}

func newLineOutput(out io.Writer) logOutput {
	return &lineOutput{
		buf: make([]byte, 0),
		out: out,
	}
}

func (l *lineOutput) Write(m logMessage) {
	l.mu.Lock()
	defer l.mu.Unlock()
	buf := l.buf[:0]
	buf = append(buf, levelChar[m.level], ' ')
	buf = m.timestamp.AppendFormat(buf, time.RFC3339)
	buf = append(buf, ' ')
	buf = append(buf, m.file...)
	buf = append(buf, ':')
	buf = strconv.AppendInt(buf, int64(m.line), 10)
	buf = append(buf, ' ')
	buf = append(buf, m.msg...)
	buf = append(buf, '\n')
	l.buf = buf
	_, _ = l.out.Write(buf)
}
