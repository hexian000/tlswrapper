package slog

import (
	"fmt"
	"os"
	"sync"
)

const (
	LevelSilence = iota
	LevelFatal
	LevelError
	LevelWarning
	LevelNotice
	LevelInfo
	LevelDebug
	LevelVerbose
)

var levelChar = [...]byte{
	'-', 'F', 'E', 'W', 'N', 'I', 'D', 'V',
}

type Logger struct {
	out   logOutput
	mu    sync.RWMutex
	level int
}

var std = &Logger{
	out:   newLineOutput(os.Stdout),
	level: LevelVerbose,
}

func Default() *Logger {
	return std
}

func Verbose(v ...interface{}) {
	std.Output(2, LevelVerbose, []byte(fmt.Sprint(v...)))
}

func Verbosef(format string, v ...interface{}) {
	std.Output(2, LevelVerbose, []byte(fmt.Sprintf(format, v...)))
}

func Debug(v ...interface{}) {
	std.Output(2, LevelDebug, []byte(fmt.Sprint(v...)))
}

func Debugf(format string, v ...interface{}) {
	std.Output(2, LevelDebug, []byte(fmt.Sprintf(format, v...)))
}

func Notice(v ...interface{}) {
	std.Output(2, LevelNotice, []byte(fmt.Sprint(v...)))
}

func Noticef(format string, v ...interface{}) {
	std.Output(2, LevelNotice, []byte(fmt.Sprintf(format, v...)))
}

func Info(v ...interface{}) {
	std.Output(2, LevelInfo, []byte(fmt.Sprint(v...)))
}

func Infof(format string, v ...interface{}) {
	std.Output(2, LevelInfo, []byte(fmt.Sprintf(format, v...)))
}

func Warning(v ...interface{}) {
	std.Output(2, LevelWarning, []byte(fmt.Sprint(v...)))
}

func Warningf(format string, v ...interface{}) {
	std.Output(2, LevelWarning, []byte(fmt.Sprintf(format, v...)))
}

func Error(v ...interface{}) {
	std.Output(2, LevelError, []byte(fmt.Sprint(v...)))
}

func Errorf(format string, v ...interface{}) {
	std.Output(2, LevelError, []byte(fmt.Sprintf(format, v...)))
}

func Fatal(v ...interface{}) {
	std.Output(2, LevelFatal, []byte(fmt.Sprint(v...)))
}

func Fatalf(format string, v ...interface{}) {
	std.Output(2, LevelFatal, []byte(fmt.Sprintf(format, v...)))
}

func Output(calldepth int, level int, msg []byte) {
	std.Output(calldepth+1, level, msg)
}
