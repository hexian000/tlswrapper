package slog

import (
	"fmt"
	"io"
	"net"
	"net/url"
	"path"
	"runtime"
	"time"
)

func (l *Logger) SetOutputConfig(output, tag string) error {
	if newOutput, ok := builtinOutput[output]; ok {
		o, err := newOutput(tag)
		if err != nil {
			return err
		}
		l.setOutput(o)
		return nil
	}
	// otherwise, the string must be a url
	u, err := url.Parse(output)
	if err != nil {
		return fmt.Errorf("unsupported log output: %s", output)
	}
	conn, err := net.Dial(u.Scheme, u.Host)
	if err != nil {
		return err
	}
	l.setOutput(newLineOutput(conn))
	return nil
}

func (l *Logger) Output(calldepth int, level int, msg []byte) {
	now := time.Now()
	out := func() logOutput {
		l.mu.RLock()
		defer l.mu.RUnlock()
		if level > l.level {
			return nil
		}
		return l.out
	}()
	if out == nil {
		return
	}
	_, file, line, ok := runtime.Caller(calldepth)
	if !ok {
		file, line = "???", 0
	} else {
		file = path.Base(file)
	}

	l.out.Write(logMessage{
		timestamp: now,
		level:     level,
		file:      []byte(file),
		line:      line,
		msg:       msg,
	})
}

func (l *Logger) SetLevel(level int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = level
}

func (l *Logger) CheckLevel(level int) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return level <= l.level
}

func (l *Logger) setOutput(out logOutput) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.out = out
}

func (l *Logger) SetOutput(w io.Writer) {
	l.setOutput(newLineOutput(w))
}

func (l *Logger) Verbose(v ...interface{}) {
	l.Output(2, LevelVerbose, []byte(fmt.Sprint(v...)))
}

func (l *Logger) Verbosef(format string, v ...interface{}) {
	l.Output(2, LevelVerbose, []byte(fmt.Sprintf(format, v...)))
}

func (l *Logger) Debug(v ...interface{}) {
	l.Output(2, LevelDebug, []byte(fmt.Sprint(v...)))
}

func (l *Logger) Debugf(format string, v ...interface{}) {
	l.Output(2, LevelDebug, []byte(fmt.Sprintf(format, v...)))
}

func (l *Logger) Info(v ...interface{}) {
	l.Output(2, LevelInfo, []byte(fmt.Sprint(v...)))
}

func (l *Logger) Infof(format string, v ...interface{}) {
	l.Output(2, LevelInfo, []byte(fmt.Sprintf(format, v...)))
}

func (l *Logger) Warning(v ...interface{}) {
	l.Output(2, LevelWarning, []byte(fmt.Sprint(v...)))
}

func (l *Logger) Warningf(format string, v ...interface{}) {
	l.Output(2, LevelWarning, []byte(fmt.Sprintf(format, v...)))
}

func (l *Logger) Error(v ...interface{}) {
	l.Output(2, LevelError, []byte(fmt.Sprint(v...)))
}

func (l *Logger) Errorf(format string, v ...interface{}) {
	l.Output(2, LevelError, []byte(fmt.Sprintf(format, v...)))
}

func (l *Logger) Fatal(v ...interface{}) {
	l.Output(2, LevelFatal, []byte(fmt.Sprint(v...)))
}

func (l *Logger) Fatalf(format string, v ...interface{}) {
	l.Output(2, LevelFatal, []byte(fmt.Sprintf(format, v...)))
}
