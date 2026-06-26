// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h3mux

import (
	"github.com/quic-go/quic-go/qlog"
	"github.com/quic-go/quic-go/qlogwriter"

	"github.com/hexian000/tlswrapper/v4/mux"
)

// wireTrace implements qlogwriter.Trace to accumulate per-connection wire
// bytes (full QUIC packet sizes, including framing and AEAD overhead) into
// SessionMetrics.  It is installed via quic.Config.Tracer, which quic-go
// invokes once per connection.
type wireTrace struct {
	metrics *mux.SessionMetrics
}

func newWireTrace(m *mux.SessionMetrics) *wireTrace { return &wireTrace{metrics: m} }

func (t *wireTrace) AddProducer() qlogwriter.Recorder {
	return &wireRecorder{metrics: t.metrics}
}

func (t *wireTrace) SupportsSchemas(string) bool { return true }

type wireRecorder struct {
	metrics *mux.SessionMetrics
}

func (r *wireRecorder) RecordEvent(ev qlogwriter.Event) {
	switch e := ev.(type) {
	case qlog.PacketSent:
		r.metrics.WireLengthSent.Add(uint64(e.Raw.Length))
	case qlog.PacketReceived:
		r.metrics.WireLengthReceived.Add(uint64(e.Raw.Length))
	}
}

func (r *wireRecorder) Close() error { return nil }
