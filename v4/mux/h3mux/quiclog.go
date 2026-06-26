// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h3mux

import (
	"log"
	"strings"

	"github.com/hexian000/gosnippets/slog"
)

func init() {
	// Strip the standard library's own timestamp prefix; slog provides its own.
	log.SetFlags(0)
	log.SetOutput(&quicLogWriter{})
}

// quicLogWriter bridges Go's standard library log output to slog.
//
// quic-go routes all of its logging through the standard log package:
//   - UDP receive/send buffer warnings   (sys_conn.go)
//   - invalid OOB packet info warnings   (sys_conn_oob.go)
//   - qlog export / directory failures   (qlog/, qlogwriter/)
//   - the DefaultLogger facade           (internal/utils/log.go)
//
// utils.DefaultLogger lives in an internal package and cannot be substituted
// from outside the quic-go module tree. Redirecting log's output is therefore
// the only single-point hook that captures every log path.
type quicLogWriter struct{}

// Write implements io.Writer.
//
// Each call carries one complete log line (including trailing newline) from
// quic-go.  We strip the newline, guard against empty strings, and forward
// the message to slog at LevelWarning.
//
// Calldepth is 0 so that the file:line annotation in the slog output points
// here (the bridge).  Recovering the original quic-go call site would require
// hard-coding knowledge of the standard library's internal stack layout and
// is deliberately avoided.
func (w *quicLogWriter) Write(p []byte) (int, error) {
	if !slog.CheckLevel(slog.LevelWarning) {
		return len(p), nil
	}
	msg := strings.TrimRight(string(p), "\n")
	if len(msg) == 0 {
		return len(p), nil
	}
	slog.Println(0, slog.LevelWarning, nil, msg)
	return len(p), nil
}
