package slog

import "log"

type wrapper struct {
	*Logger
	level int
}

func (w *wrapper) Write(p []byte) (n int, err error) {
	const calldepth = 4
	w.Output(calldepth, w.level, p)
	return len(p), nil
}

func Wrap(logger *Logger, level int) *log.Logger {
	return log.New(&wrapper{logger, level}, "", 0)
}
