package slog

import (
	"fmt"
	"log"
	"os"
	"path"
	"runtime"
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

var levelChar = [...]rune{
	'V', 'D', 'I', 'W', 'E', 'F',
}

type Logger struct {
	*log.Logger
	Level int
}

var std = &Logger{log.New(os.Stderr, "", 0), LevelVerbose}

func Default() *Logger {
	return std
}

func (l *Logger) output(calldepth int, level int, message string) {
	if level < l.Level {
		return
	}
	now := time.Now()
	_, file, line, ok := runtime.Caller(calldepth)
	if !ok {
		file, line = "<unknown>", 0
	} else {
		file = path.Base(file)
	}
	l.Printf("%c %s %s:%d %s\n", levelChar[level], now.Format(time.RFC3339Nano), file, line, message)
}

func (l *Logger) Verbose(v ...interface{}) {
	l.output(2, LevelVerbose, fmt.Sprint(v...))
}

func (l *Logger) Verbosef(format string, v ...interface{}) {
	l.output(2, LevelVerbose, fmt.Sprintf(format, v...))
}

func (l *Logger) Debug(v ...interface{}) {
	l.output(2, LevelDebug, fmt.Sprint(v...))
}

func (l *Logger) Debugf(format string, v ...interface{}) {
	l.output(2, LevelDebug, fmt.Sprintf(format, v...))
}

func (l *Logger) Info(v ...interface{}) {
	l.output(2, LevelInfo, fmt.Sprint(v...))
}

func (l *Logger) Infof(format string, v ...interface{}) {
	l.output(2, LevelInfo, fmt.Sprintf(format, v...))
}

func (l *Logger) Warning(v ...interface{}) {
	l.output(2, LevelWarning, fmt.Sprint(v...))
}

func (l *Logger) Warningf(format string, v ...interface{}) {
	l.output(2, LevelWarning, fmt.Sprintf(format, v...))
}

func (l *Logger) Error(v ...interface{}) {
	l.output(2, LevelError, fmt.Sprint(v...))
}

func (l *Logger) Errorf(format string, v ...interface{}) {
	l.output(2, LevelError, fmt.Sprintf(format, v...))
}

func (l *Logger) Fatal(v ...interface{}) {
	l.output(2, LevelFatal, fmt.Sprint(v...))
}

func (l *Logger) Fatalf(format string, v ...interface{}) {
	l.output(2, LevelFatal, fmt.Sprintf(format, v...))
}

func Verbose(v ...interface{}) {
	std.output(2, LevelVerbose, fmt.Sprint(v...))
}

func Verbosef(format string, v ...interface{}) {
	std.output(2, LevelVerbose, fmt.Sprintf(format, v...))
}

func Debug(v ...interface{}) {
	std.output(2, LevelDebug, fmt.Sprint(v...))
}

func Debugf(format string, v ...interface{}) {
	std.output(2, LevelDebug, fmt.Sprintf(format, v...))
}

func Info(v ...interface{}) {
	std.output(2, LevelInfo, fmt.Sprint(v...))
}

func Infof(format string, v ...interface{}) {
	std.output(2, LevelInfo, fmt.Sprintf(format, v...))
}

func Warning(v ...interface{}) {
	std.output(2, LevelWarning, fmt.Sprint(v...))
}

func Warningf(format string, v ...interface{}) {
	std.output(2, LevelWarning, fmt.Sprintf(format, v...))
}

func Error(v ...interface{}) {
	std.output(2, LevelError, fmt.Sprint(v...))
}

func Errorf(format string, v ...interface{}) {
	std.output(2, LevelError, fmt.Sprintf(format, v...))
}

func Fatal(v ...interface{}) {
	std.output(2, LevelFatal, fmt.Sprint(v...))
}

func Fatalf(format string, v ...interface{}) {
	std.output(2, LevelFatal, fmt.Sprintf(format, v...))
}
