package slog

import (
	"fmt"
	"io"
	"os"
	"path"
	"runtime"
	"strconv"
	"sync"
	"time"
)

const (
	LevelVerbose = iota
	LevelDebug
	LevelInfo
	LevelWarning
	LevelError
	LevelFatal
	LevelSilence
)

const ISO8601Milli = "2006-01-02T15:04:05.000Z07:00"

var levelChar = [...]byte{
	'V', 'D', 'I', 'W', 'E', 'F',
}

type Logger struct {
	out   io.Writer
	mu    sync.Mutex
	level int
	buf   []byte
}

var std = &Logger{out: os.Stderr}

func Default() *Logger {
	return std
}

func (l *Logger) SetLevel(level int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.level = level
}

func (l *Logger) SetOutput(out io.Writer) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.out = out
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
	buf = now.AppendFormat(buf, ISO8601Milli)
	buf = append(buf, ' ')
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

func (l *Logger) Verbose(v ...interface{}) {
	l.Output(2, LevelVerbose, fmt.Sprintln(v...))
}

func (l *Logger) Verbosef(format string, v ...interface{}) {
	l.Output(2, LevelVerbose, fmt.Sprintf(format, v...))
}

func (l *Logger) Debug(v ...interface{}) {
	l.Output(2, LevelDebug, fmt.Sprintln(v...))
}

func (l *Logger) Debugf(format string, v ...interface{}) {
	l.Output(2, LevelDebug, fmt.Sprintf(format, v...))
}

func (l *Logger) Info(v ...interface{}) {
	l.Output(2, LevelInfo, fmt.Sprintln(v...))
}

func (l *Logger) Infof(format string, v ...interface{}) {
	l.Output(2, LevelInfo, fmt.Sprintf(format, v...))
}

func (l *Logger) Warning(v ...interface{}) {
	l.Output(2, LevelWarning, fmt.Sprintln(v...))
}

func (l *Logger) Warningf(format string, v ...interface{}) {
	l.Output(2, LevelWarning, fmt.Sprintf(format, v...))
}

func (l *Logger) Error(v ...interface{}) {
	l.Output(2, LevelError, fmt.Sprintln(v...))
}

func (l *Logger) Errorf(format string, v ...interface{}) {
	l.Output(2, LevelError, fmt.Sprintf(format, v...))
}

func (l *Logger) Fatal(v ...interface{}) {
	l.Output(2, LevelFatal, fmt.Sprintln(v...))
}

func (l *Logger) Fatalf(format string, v ...interface{}) {
	l.Output(2, LevelFatal, fmt.Sprintf(format, v...))
}

func Verbose(v ...interface{}) {
	std.Output(2, LevelVerbose, fmt.Sprintln(v...))
}

func Verbosef(format string, v ...interface{}) {
	std.Output(2, LevelVerbose, fmt.Sprintf(format, v...))
}

func Debug(v ...interface{}) {
	std.Output(2, LevelDebug, fmt.Sprintln(v...))
}

func Debugf(format string, v ...interface{}) {
	std.Output(2, LevelDebug, fmt.Sprintf(format, v...))
}

func Info(v ...interface{}) {
	std.Output(2, LevelInfo, fmt.Sprintln(v...))
}

func Infof(format string, v ...interface{}) {
	std.Output(2, LevelInfo, fmt.Sprintf(format, v...))
}

func Warning(v ...interface{}) {
	std.Output(2, LevelWarning, fmt.Sprintln(v...))
}

func Warningf(format string, v ...interface{}) {
	std.Output(2, LevelWarning, fmt.Sprintf(format, v...))
}

func Error(v ...interface{}) {
	std.Output(2, LevelError, fmt.Sprintln(v...))
}

func Errorf(format string, v ...interface{}) {
	std.Output(2, LevelError, fmt.Sprintf(format, v...))
}

func Fatal(v ...interface{}) {
	std.Output(2, LevelFatal, fmt.Sprintln(v...))
}

func Fatalf(format string, v ...interface{}) {
	std.Output(2, LevelFatal, fmt.Sprintf(format, v...))
}

func Output(calldepth int, level int, message string) {
	std.Output(calldepth+1, level, message)
}
