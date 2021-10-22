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

const ISO8601Milli = "2006-01-02T15:04:05.000Z07:00"

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
		file, line = "???", 0
	} else {
		file = path.Base(file)
	}
	l.Printf("%c %s %s:%d %s", levelChar[level], now.Format(ISO8601Milli), file, line, message)
}

func (l *Logger) Verbose(v ...interface{}) {
	l.output(2, LevelVerbose, fmt.Sprintln(v...))
}

func (l *Logger) Verbosef(format string, v ...interface{}) {
	l.output(2, LevelVerbose, fmt.Sprintf(format+"\n", v...))
}

func (l *Logger) Debug(v ...interface{}) {
	l.output(2, LevelDebug, fmt.Sprintln(v...))
}

func (l *Logger) Debugf(format string, v ...interface{}) {
	l.output(2, LevelDebug, fmt.Sprintf(format+"\n", v...))
}

func (l *Logger) Info(v ...interface{}) {
	l.output(2, LevelInfo, fmt.Sprintln(v...))
}

func (l *Logger) Infof(format string, v ...interface{}) {
	l.output(2, LevelInfo, fmt.Sprintf(format+"\n", v...))
}

func (l *Logger) Warning(v ...interface{}) {
	l.output(2, LevelWarning, fmt.Sprintln(v...))
}

func (l *Logger) Warningf(format string, v ...interface{}) {
	l.output(2, LevelWarning, fmt.Sprintf(format+"\n", v...))
}

func (l *Logger) Error(v ...interface{}) {
	l.output(2, LevelError, fmt.Sprintln(v...))
}

func (l *Logger) Errorf(format string, v ...interface{}) {
	l.output(2, LevelError, fmt.Sprintf(format+"\n", v...))
}

func (l *Logger) Fatal(v ...interface{}) {
	l.output(2, LevelFatal, fmt.Sprintln(v...))
}

func (l *Logger) Fatalf(format string, v ...interface{}) {
	l.output(2, LevelFatal, fmt.Sprintf(format+"\n", v...))
}

func Verbose(v ...interface{}) {
	std.output(2, LevelVerbose, fmt.Sprintln(v...))
}

func Verbosef(format string, v ...interface{}) {
	std.output(2, LevelVerbose, fmt.Sprintf(format+"\n", v...))
}

func Debug(v ...interface{}) {
	std.output(2, LevelDebug, fmt.Sprintln(v...))
}

func Debugf(format string, v ...interface{}) {
	std.output(2, LevelDebug, fmt.Sprintf(format+"\n", v...))
}

func Info(v ...interface{}) {
	std.output(2, LevelInfo, fmt.Sprintln(v...))
}

func Infof(format string, v ...interface{}) {
	std.output(2, LevelInfo, fmt.Sprintf(format+"\n", v...))
}

func Warning(v ...interface{}) {
	std.output(2, LevelWarning, fmt.Sprintln(v...))
}

func Warningf(format string, v ...interface{}) {
	std.output(2, LevelWarning, fmt.Sprintf(format+"\n", v...))
}

func Error(v ...interface{}) {
	std.output(2, LevelError, fmt.Sprintln(v...))
}

func Errorf(format string, v ...interface{}) {
	std.output(2, LevelError, fmt.Sprintf(format+"\n", v...))
}

func Fatal(v ...interface{}) {
	std.output(2, LevelFatal, fmt.Sprintln(v...))
}

func Fatalf(format string, v ...interface{}) {
	std.output(2, LevelFatal, fmt.Sprintf(format+"\n", v...))
}
