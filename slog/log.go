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
	LevelInfo
	LevelDebug
	LevelVerbose
)

var levelChar = [...]byte{
	' ', 'F', 'E', 'W', 'I', 'D', 'V',
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
