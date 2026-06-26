// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package h3mux

import (
	"testing"

	"github.com/quic-go/quic-go/qlog"

	"github.com/hexian000/tlswrapper/v4/mux"
)

// TestWireTraceRecorder verifies that the wireTrace recorder accumulates QUIC
// packet sizes into SessionMetrics and that the qlog trace plumbing methods
// behave as the qlogwriter.Trace contract requires.
func TestWireTraceRecorder(t *testing.T) {
	m := &mux.SessionMetrics{}
	tr := newWireTrace(m)

	if !tr.SupportsSchemas("anything") {
		t.Fatal("SupportsSchemas() = false, want true")
	}

	rec := tr.AddProducer()
	if rec == nil {
		t.Fatal("AddProducer() = nil")
	}

	rec.RecordEvent(qlog.PacketSent{Raw: qlog.RawInfo{Length: 100}})
	rec.RecordEvent(qlog.PacketReceived{Raw: qlog.RawInfo{Length: 40}})
	// An unrelated event must not affect the counters.
	rec.RecordEvent(qlog.PacketDropped{})

	if got := m.WireLengthSent.Load(); got != 100 {
		t.Fatalf("WireLengthSent = %d, want 100", got)
	}
	if got := m.WireLengthReceived.Load(); got != 40 {
		t.Fatalf("WireLengthReceived = %d, want 40", got)
	}
	if err := rec.Close(); err != nil {
		t.Fatalf("Close() = %v, want nil", err)
	}
}
