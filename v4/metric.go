// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package tlswrapper

import (
	"bytes"
	"cmp"
	"fmt"
	"io"
	"math"
	"mime"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"runtime/debug"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/hexian000/gosnippets/formats"
	"github.com/hexian000/gosnippets/slog"
	"github.com/hexian000/tlswrapper/v4/config"
)

const maxContentLength = 4194304

func setRespHeader(h http.Header, mimeType string, nocache bool) {
	h.Set("Content-Type", mime.FormatMediaType(mimeType, map[string]string{"charset": "utf-8"}))
	h.Set("X-Content-Type-Options", "nosniff")
	if nocache {
		h.Set("Cache-Control", "no-store")
	}
}

func fprintf(w io.Writer, format string, v ...any) {
	_, err := fmt.Fprintf(w, format, v...)
	if err != nil {
		panic(err)
	}
}

type apiConfigHandler struct {
	s *Server
}

// ServeHTTP handles configuration update requests
func (h *apiConfigHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
	if r.ContentLength > maxContentLength {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	b := make([]byte, r.ContentLength)
	n, err := io.ReadFull(r.Body, b)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(formats.Error(err)))
		return
	}
	cfg, err := config.Load(b[:n])
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(formats.Error(err)))
		return
	}
	if err := h.s.LoadConfig(cfg); err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(formats.Error(err)))
		return
	}
}

func printMemStats(w io.Writer) {
	var memstats runtime.MemStats
	runtime.ReadMemStats(&memstats)
	fprintf(w, "%-20s: %s ≤ %s (%s objects)\n", "Heap In-use",
		formats.IECBytes(float64(memstats.HeapInuse)),
		formats.IECBytes(float64(memstats.HeapSys-memstats.HeapReleased)),
		formats.SIPrefix(float64(memstats.HeapObjects)))
	fprintf(w, "%-20s: %s ≤ %s\n", "Stack In-use",
		formats.IECBytes(float64(memstats.StackInuse)),
		formats.IECBytes(float64(memstats.StackSys)))
	runtimeSys := memstats.MSpanSys + memstats.MCacheSys + memstats.BuckHashSys + memstats.GCSys + memstats.OtherSys
	fprintf(w, "%-20s: %s\n", "Runtime Allocated", formats.IECBytes(float64(runtimeSys)))
	fprintf(w, "%-20s: %s (+%s)\n", "Total Allocated",
		formats.IECBytes(float64(memstats.Sys-memstats.HeapReleased)),
		formats.IECBytes(float64(memstats.HeapReleased)))
	fprintf(w, "%-20s: %s < %s\n", "Next GC",
		formats.IECBytes(float64(memstats.HeapAlloc)),
		formats.IECBytes(float64(memstats.NextGC)))
	numGC := memstats.NumGC
	if numGC > 0 {
		numStats := uint32(len(memstats.PauseNs))
		if numGC > numStats {
			numGC = numStats
		}
		t := time.Now().UnixNano()
		b := &bytes.Buffer{}
		for i := uint32(0); i < 3; i++ {
			if i >= numGC {
				break
			}
			idx := (memstats.NumGC + (numStats - 1) - i) % numStats
			pauseEnd := int64(memstats.PauseEnd[idx])
			pauseAt := time.Duration(t - pauseEnd)
			if i == 0 {
				fmt.Fprintf(b, "~%s %s", formats.Duration(pauseAt),
					formats.Duration(time.Duration(memstats.PauseNs[idx])))
			} else {
				fmt.Fprintf(b, ", %s %s", formats.Duration(pauseAt),
					formats.Duration(time.Duration(memstats.PauseNs[idx])))
			}
			t = pauseEnd
		}
		fprintf(w, "%-20s: %s\n", "Recent GC", b.String())
		pause := make([]time.Duration, 0, numGC)
		for i := uint32(0); i < numGC; i++ {
			idx := (memstats.NumGC + (numStats - 1) - i) % numStats
			pause = append(pause, time.Duration(memstats.PauseNs[idx]))
		}
		slices.SortFunc(pause, cmp.Compare)
		i50 := int(math.Floor(float64(numGC) * 0.50))
		i90 := int(math.Floor(float64(numGC) * 0.90))
		p50, p90, pmax := pause[i50], pause[i90], pause[numGC-1]
		fprintf(w, "%-20s: P50=%s P90=%s MAX=%s TOTAL=%s\n", "GC Pause",
			formats.Duration(p50), formats.Duration(p90), formats.Duration(pmax),
			formats.Duration(time.Duration(memstats.PauseTotalNs)))
	} else {
		fprintf(w, "%-20s: %s\n", "Recent GC", "(never)")
		fprintf(w, "%-20s: %s\n", "GC Pause", "(never)")
	}
	fprintf(w, "%-20s: %.07f%%\n", "GC CPU Fraction",
		memstats.GCCPUFraction*1e+2/float64(runtime.GOMAXPROCS(0)))
}

type apiStats struct {
	Accepted   uint64
	Served     uint64
	ReqTotal   uint64
	ReqSuccess uint64
	Rx, Tx     uint64
	Timestamp  time.Time
}

type apiStatsHandler struct {
	s    *Server
	last apiStats
}

// ServeHTTP handles statistics requests
func (h *apiStatsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var stateless bool
	switch r.Method {
	case http.MethodGet:
		stateless = true
	case http.MethodPost:
		stateless = false
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	uri, err := url.ParseRequestURI(r.RequestURI)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		slog.Debugf("uri: %s", formats.Error(err))
		return
	}
	query, err := url.ParseQuery(uri.RawQuery)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		slog.Debugf("uri: %s", formats.Error(err))
		return
	}
	nobanner := false
	if s := query.Get("nobanner"); s != "" {
		if b, err := strconv.ParseBool(s); err == nil {
			nobanner = b
		}
	}
	rt := false
	if s := query.Get("runtime"); s != "" {
		if b, err := strconv.ParseBool(s); err == nil {
			rt = b
		}
	}
	evlog := 10
	if s := query.Get("eventlog"); s != "" {
		if i, err := strconv.ParseInt(s, 10, 0); err == nil {
			evlog = int(i)
		}
	}

	now := time.Now()
	uptime := now.Sub(h.s.started)
	setRespHeader(w.Header(), "text/plain", stateless)
	w.WriteHeader(http.StatusOK)
	if !nobanner {
		fprintf(w, "tlswrapper %s\n  %s\n\n", Version, Homepage)
	}
	fprintf(w, "%-20s: %s\n", "Server Time", now.Format(slog.TimeLayout))
	fprintf(w, "%-20s: %s\n", "Uptime", formats.Duration(uptime))
	if rt {
		fprintf(w, "%-20s: %v\n", "Max Procs", runtime.GOMAXPROCS(-1))
		fprintf(w, "%-20s: %v\n", "Num Goroutines", runtime.NumGoroutine())
		printMemStats(w)
	}
	stats := h.s.Stats()
	fprintf(w, "%-20s: %d (%d streams)\n", "Num Sessions", stats.NumSessions, stats.NumStreams)
	fprintf(w, "%-20s: Rx %s, Tx %s\n", "Traffic",
		formats.IECBytes(float64(stats.Rx)), formats.IECBytes(float64(stats.Tx)))
	rejected := stats.Accepted - stats.Served
	fprintf(w, "%-20s: %d (%+d rejected)\n", "Listener Accepts",
		stats.Served, rejected)
	fprintf(w, "%-20s: %d (%+d)\n", "Authorizations",
		stats.Authorized, stats.Served-stats.Authorized)
	fprintf(w, "%-20s: %d (%+d)\n", "Requests",
		stats.ReqSuccess, stats.ReqTotal-stats.ReqSuccess)

	if !stateless {
		dt := now.Sub(h.last.Timestamp).Seconds()

		fprintf(w, "%-20s: %.1f/s (%+.1f/s rejected)\n", "Authorized",
			float64(stats.Served-h.last.Served)/dt,
			float64((stats.Accepted-stats.Served)-(h.last.Accepted-h.last.Served))/dt)
		fprintf(w, "%-20s: %.1f/s (%+.1f/s)\n", "Requests",
			float64(stats.ReqSuccess-h.last.ReqSuccess)/dt,
			float64((stats.ReqTotal-stats.ReqSuccess)-(h.last.ReqTotal-h.last.ReqSuccess))/dt)
		fprintf(w, "%-20s: Rx %s/s, Tx %s/s\n", "Bandwidth",
			formats.IECBytes(float64(stats.Rx-h.last.Rx)/dt),
			formats.IECBytes(float64(stats.Tx-h.last.Tx)/dt))

		h.last.Accepted, h.last.Served = stats.Accepted, stats.Served
		h.last.ReqTotal, h.last.ReqSuccess = stats.ReqTotal, stats.ReqSuccess
		h.last.Rx, h.last.Tx = stats.Rx, stats.Tx
		h.last.Timestamp = now
	}

	fprintf(w, "\n> Sessions\n")
	sort.Slice(stats.sessions, func(i, j int) bool {
		return stats.sessions[i].Name < stats.sessions[j].Name
	})
	for _, ss := range stats.sessions {
		if (ss.LastChanged != time.Time{}) {
			status := "offline"
			if ss.Active {
				status = fmt.Sprintf("%d streams", ss.NumStreams)
			}
			fprintf(w, "%-20q: %s %s\n", ss.Name, ss.LastChanged.Format(slog.TimeLayout), status)
		} else {
			fprintf(w, "%-20q: never seen\n", ss.Name)
		}
	}

	if evlog > 0 {
		fprintf(w, "\n> Recent Events\n")
		if err := h.s.recentEvents.Format(w, evlog); err != nil {
			panic(err)
		}
	}
}

// Prometheus text exposition format helpers

func sanitizeLabelValue(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

func writePromHelp(w io.Writer, name, help string) {
	fprintf(w, "# HELP %s %s\n", name, help)
}

func writePromType(w io.Writer, name, typ string) {
	fprintf(w, "# TYPE %s %s\n", name, typ)
}

func writePromGauge(w io.Writer, name, help string, value float64, labels ...string) {
	writePromHelp(w, name, help)
	writePromType(w, name, "gauge")
	if len(labels) > 0 {
		fprintf(w, "%s{%s} %s\n", name, strings.Join(labels, ","), formatFloat(value))
	} else {
		fprintf(w, "%s %s\n", name, formatFloat(value))
	}
}

func writePromCounter(w io.Writer, name, help string, value float64, labels ...string) {
	writePromHelp(w, name, help)
	writePromType(w, name, "counter")
	if len(labels) > 0 {
		fprintf(w, "%s{%s} %s\n", name, strings.Join(labels, ","), formatFloat(value))
	} else {
		fprintf(w, "%s %s\n", name, formatFloat(value))
	}
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func label(key, value string) string {
	return fmt.Sprintf(`%s="%s"`, key, sanitizeLabelValue(value))
}

type apiMetricsHandler struct {
	s *Server
}

// ServeHTTP handles Prometheus-compatible metrics requests
func (h *apiMetricsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	now := time.Now()
	stats := h.s.Stats()

	setRespHeader(w.Header(), "text/plain", true)
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)

	// Application metrics
	writePromGauge(w, "tlswrapper_uptime_seconds", "Server uptime in seconds.",
		now.Sub(h.s.started).Seconds())
	writePromGauge(w, "tlswrapper_sessions", "Number of active sessions.",
		float64(stats.NumSessions))
	writePromGauge(w, "tlswrapper_streams", "Number of active streams.",
		float64(stats.NumStreams))
	writePromCounter(w, "tlswrapper_rx_bytes_total", "Total bytes received.",
		float64(stats.Rx))
	writePromCounter(w, "tlswrapper_tx_bytes_total", "Total bytes transmitted.",
		float64(stats.Tx))
	writePromCounter(w, "tlswrapper_accepted_total", "Total accepted connections.",
		float64(stats.Accepted))
	writePromCounter(w, "tlswrapper_served_total", "Total served connections.",
		float64(stats.Served))
	writePromCounter(w, "tlswrapper_authorized_total", "Total authorized connections.",
		float64(stats.Authorized))
	writePromCounter(w, "tlswrapper_requests_total", "Total requests.",
		float64(stats.ReqTotal))
	writePromCounter(w, "tlswrapper_requests_success_total", "Total successful requests.",
		float64(stats.ReqSuccess))

	// Per-session metrics
	for _, ss := range stats.sessions {
		sessionLabel := label("session", ss.Name)
		up := 0.0
		if ss.Active {
			up = 1.0
		}
		writePromGauge(w, "tlswrapper_session_up", "Whether the session is active (1=active, 0=offline).",
			up, sessionLabel)
		writePromGauge(w, "tlswrapper_session_streams", "Number of streams in the session.",
			float64(ss.NumStreams), sessionLabel)
	}

	// Go runtime metrics
	writePromGauge(w, "go_goroutines", "Number of goroutines.",
		float64(runtime.NumGoroutine()))
	writePromGauge(w, "go_info", "Go version information.",
		1.0, label("version", runtime.Version()))

	var memstats runtime.MemStats
	runtime.ReadMemStats(&memstats)

	writePromGauge(w, "go_memstats_alloc_bytes", "Bytes allocated and still in use.",
		float64(memstats.Alloc))
	writePromCounter(w, "go_memstats_alloc_bytes_total", "Total bytes ever allocated (even if freed).",
		float64(memstats.TotalAlloc))
	writePromGauge(w, "go_memstats_sys_bytes", "Total bytes obtained from the OS.",
		float64(memstats.Sys))
	writePromGauge(w, "go_memstats_heap_alloc_bytes", "Heap bytes allocated and still in use.",
		float64(memstats.HeapAlloc))
	writePromGauge(w, "go_memstats_heap_sys_bytes", "Heap bytes obtained from the OS.",
		float64(memstats.HeapSys))
	writePromGauge(w, "go_memstats_heap_inuse_bytes", "Heap bytes in use.",
		float64(memstats.HeapInuse))
	writePromGauge(w, "go_memstats_heap_released_bytes", "Heap bytes released to the OS.",
		float64(memstats.HeapReleased))
	writePromGauge(w, "go_memstats_stack_inuse_bytes", "Stack bytes in use.",
		float64(memstats.StackInuse))
	writePromGauge(w, "go_memstats_stack_sys_bytes", "Stack bytes obtained from the OS.",
		float64(memstats.StackSys))
	writePromGauge(w, "go_memstats_next_gc_bytes", "Target heap size for next GC cycle.",
		float64(memstats.NextGC))
	writePromGauge(w, "go_memstats_gc_cpu_fraction", "GC CPU fraction.",
		memstats.GCCPUFraction)
	writePromGauge(w, "go_memstats_gc_sys_bytes", "GC metadata bytes.",
		float64(memstats.GCSys))
	writePromGauge(w, "go_memstats_last_gc_time_seconds", "Last GC timestamp as Unix seconds.",
		float64(memstats.LastGC)/1e9)

	// GC duration summary
	writePromHelp(w, "go_gc_duration_seconds", "GC pause durations.")
	writePromType(w, "go_gc_duration_seconds", "summary")
	numGC := memstats.NumGC
	if numGC > 0 {
		numStats := uint32(len(memstats.PauseNs))
		if numGC > numStats {
			numGC = numStats
		}
		pause := make([]time.Duration, 0, numGC)
		for i := uint32(0); i < numGC; i++ {
			idx := (memstats.NumGC + (numStats - 1) - i) % numStats
			pause = append(pause, time.Duration(memstats.PauseNs[idx]))
		}
		slices.SortFunc(pause, cmp.Compare)
		i50 := int(math.Floor(float64(numGC) * 0.50))
		i90 := int(math.Floor(float64(numGC) * 0.90))
		fprintf(w, "go_gc_duration_seconds{quantile=\"0.5\"} %s\n", formatFloat(pause[i50].Seconds()))
		fprintf(w, "go_gc_duration_seconds{quantile=\"0.9\"} %s\n", formatFloat(pause[i90].Seconds()))
		fprintf(w, "go_gc_duration_seconds{quantile=\"1\"} %s\n", formatFloat(pause[numGC-1].Seconds()))
		fprintf(w, "go_gc_duration_seconds_sum %s\n", formatFloat(time.Duration(memstats.PauseTotalNs).Seconds()))
		fprintf(w, "go_gc_duration_seconds_count %d\n", numGC)
	} else {
		fprintf(w, "go_gc_duration_seconds{quantile=\"0.5\"} 0\n")
		fprintf(w, "go_gc_duration_seconds{quantile=\"0.9\"} 0\n")
		fprintf(w, "go_gc_duration_seconds{quantile=\"1\"} 0\n")
		fprintf(w, "go_gc_duration_seconds_sum 0\n")
		fprintf(w, "go_gc_duration_seconds_count 0\n")
	}
}

// RunHTTPServer runs an HTTP server for metrics and configuration
func RunHTTPServer(l net.Listener, s *Server) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthy", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/config", &apiConfigHandler{s: s})
	mux.Handle("/stats", &apiStatsHandler{s: s})
	mux.Handle("/metrics", &apiMetricsHandler{s: s})
	mux.HandleFunc("/gc", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		setRespHeader(w.Header(), "text/plain", false)
		w.WriteHeader(http.StatusOK)
		start := time.Now()
		debug.FreeOSMemory()
		printMemStats(w)
		fprintf(w, "%-20s: %s\n", "Time Cost", formats.Duration(time.Since(start)))
	})
	mux.HandleFunc("/stack", func(w http.ResponseWriter, r *http.Request) {
		setRespHeader(w.Header(), "text/plain", true)
		w.WriteHeader(http.StatusOK)
		var buf [65536]byte
		n := runtime.Stack(buf[:], true)
		fprintf(w, "%s\n", string(buf[:n]))
	})
	server := &http.Server{
		Handler:  mux,
		ErrorLog: slog.Wrap(slog.Default(), slog.LevelError),
	}
	return server.Serve(l)
}
