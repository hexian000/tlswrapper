package slog

import "log"

type wrapper struct {
	*Logger
}

func (w *wrapper) Write(p []byte) (n int, err error) {
	const calldepth = 4
	w.Output(calldepth, LevelError, string(p))
	return len(p), nil
}

func Wrap(logger *Logger) *log.Logger {
	return log.New(&wrapper{logger}, "", 0)
}
