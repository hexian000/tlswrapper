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
	"time"

	"github.com/hexian000/gosnippets/formats"
	"github.com/hexian000/gosnippets/slog"
	"github.com/hexian000/tlswrapper/v4/config"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
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

// ServeHTTP accepts POSTed config snapshots and applies ReloadConfig.
func (h *apiConfigHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
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
	if err := h.s.ReloadConfig(cfg); err != nil {
		// ReloadConfig currently logs partial failures and returns nil.
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

// ServeHTTP renders human-readable stats; POST also includes rate deltas.
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
	var streamsStarted, streamsSucceeded, streamsFailed uint64
	var bytesSent, bytesReceived, wireLengthSent, wireLengthReceived uint64
	for _, ss := range stats.sessions {
		streamsStarted += ss.StreamsStarted
		streamsSucceeded += ss.StreamsSucceeded
		streamsFailed += ss.StreamsFailed
		bytesSent += ss.BytesSent
		bytesReceived += ss.BytesReceived
		wireLengthSent += ss.WireLengthSent
		wireLengthReceived += ss.WireLengthReceived
	}
	numStreams := streamsStarted - (streamsSucceeded + streamsFailed)
	fprintf(w, "%-20s: %d (%d streams)\n", "Num Sessions", stats.NumSessions, numStreams)
	fprintf(w, "%-20s: %d started, %d ok, %d fail\n", "Streams",
		streamsStarted, streamsSucceeded, streamsFailed)
	fprintf(w, "%-20s: Rx %s, Tx %s\n", "Mux Traffic",
		formats.IECBytes(float64(stats.Rx)), formats.IECBytes(float64(stats.Tx)))
	fprintf(w, "%-20s: Rx %s, Tx %s\n", "Wire Traffic",
		formats.IECBytes(float64(wireLengthReceived)), formats.IECBytes(float64(wireLengthSent)))
	fprintf(w, "%-20s: Rx %s, Tx %s\n", "TCP Traffic",
		formats.IECBytes(float64(bytesReceived)), formats.IECBytes(float64(bytesSent)))
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
		fprintf(w, "%-20s: %.1f/s (%+.1f/s)\n", "Request Rate",
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
				numStreams := ss.StreamsStarted - (ss.StreamsSucceeded + ss.StreamsFailed)
				status = fmt.Sprintf("Rx %s, Tx %s; %d streams",
					formats.IECBytes(float64(ss.BytesReceived)),
					formats.IECBytes(float64(ss.BytesSent)),
					numStreams)
			}
			fprintf(w, "%-20s: %s %s\n", ss.Name, ss.LastChanged.Format(slog.TimeLayout), status)
		} else {
			fprintf(w, "%-20s: never seen\n", ss.Name)
		}
	}

	if evlog > 0 {
		fprintf(w, "\n> Recent Events\n")
		if err := h.s.recentEvents.Format(w, evlog); err != nil {
			panic(err)
		}
	}
}

type serverMetricsCollector struct {
	s *Server

	uptimeDesc          *prometheus.Desc
	sessionsDesc        *prometheus.Desc
	streamsDesc         *prometheus.Desc
	rxBytesDesc         *prometheus.Desc
	txBytesDesc         *prometheus.Desc
	acceptedDesc        *prometheus.Desc
	servedDesc          *prometheus.Desc
	authorizedDesc      *prometheus.Desc
	requestsDesc        *prometheus.Desc
	requestsSuccessDesc *prometheus.Desc
	sessionUpDesc       *prometheus.Desc
	sessionStreamsDesc  *prometheus.Desc

	sessionStreamsStartedDesc     *prometheus.Desc
	sessionStreamsSucceededDesc   *prometheus.Desc
	sessionStreamsFailedDesc      *prometheus.Desc
	sessionBytesSentDesc          *prometheus.Desc
	sessionBytesReceivedDesc      *prometheus.Desc
	sessionWireLengthSentDesc     *prometheus.Desc
	sessionWireLengthReceivedDesc *prometheus.Desc
}

func newServerMetricsCollector(s *Server) prometheus.Collector {
	return &serverMetricsCollector{
		s: s,
		uptimeDesc: prometheus.NewDesc(
			"tlswrapper_uptime_seconds",
			"Server uptime in seconds.",
			nil, nil),
		sessionsDesc: prometheus.NewDesc(
			"tlswrapper_sessions",
			"Number of active sessions.",
			nil, nil),
		streamsDesc: prometheus.NewDesc(
			"tlswrapper_streams",
			"Number of active streams.",
			nil, nil),
		rxBytesDesc: prometheus.NewDesc(
			"tlswrapper_rx_bytes_total",
			"Total bytes received.",
			nil, nil),
		txBytesDesc: prometheus.NewDesc(
			"tlswrapper_tx_bytes_total",
			"Total bytes transmitted.",
			nil, nil),
		acceptedDesc: prometheus.NewDesc(
			"tlswrapper_accepted_total",
			"Total accepted connections.",
			nil, nil),
		servedDesc: prometheus.NewDesc(
			"tlswrapper_served_total",
			"Total served connections.",
			nil, nil),
		authorizedDesc: prometheus.NewDesc(
			"tlswrapper_authorized_total",
			"Total authorized connections.",
			nil, nil),
		requestsDesc: prometheus.NewDesc(
			"tlswrapper_requests_total",
			"Total requests.",
			nil, nil),
		requestsSuccessDesc: prometheus.NewDesc(
			"tlswrapper_requests_success_total",
			"Total successful requests.",
			nil, nil),
		sessionUpDesc: prometheus.NewDesc(
			"tlswrapper_session_up",
			"Whether the session is active (1=active, 0=offline).",
			[]string{"session"}, nil),
		sessionStreamsDesc: prometheus.NewDesc(
			"tlswrapper_session_streams",
			"Number of streams in the session.",
			[]string{"session"}, nil),
		sessionStreamsStartedDesc: prometheus.NewDesc(
			"tlswrapper_session_grpc_streams_started_total",
			"Total gRPC streams started in the session.",
			[]string{"session"}, nil),
		sessionStreamsSucceededDesc: prometheus.NewDesc(
			"tlswrapper_session_grpc_streams_succeeded_total",
			"Total gRPC streams that ended successfully in the session.",
			[]string{"session"}, nil),
		sessionStreamsFailedDesc: prometheus.NewDesc(
			"tlswrapper_session_grpc_streams_failed_total",
			"Total gRPC streams that ended with an error in the session.",
			[]string{"session"}, nil),
		sessionBytesSentDesc: prometheus.NewDesc(
			"tlswrapper_session_grpc_bytes_sent_total",
			"Total payload bytes sent in the session.",
			[]string{"session"}, nil),
		sessionBytesReceivedDesc: prometheus.NewDesc(
			"tlswrapper_session_grpc_bytes_received_total",
			"Total payload bytes received in the session.",
			[]string{"session"}, nil),
		sessionWireLengthSentDesc: prometheus.NewDesc(
			"tlswrapper_session_grpc_wire_bytes_sent_total",
			"Total wire bytes sent in the session.",
			[]string{"session"}, nil),
		sessionWireLengthReceivedDesc: prometheus.NewDesc(
			"tlswrapper_session_grpc_wire_bytes_received_total",
			"Total wire bytes received in the session.",
			[]string{"session"}, nil),
	}
}

func (c *serverMetricsCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.uptimeDesc
	ch <- c.sessionsDesc
	ch <- c.streamsDesc
	ch <- c.rxBytesDesc
	ch <- c.txBytesDesc
	ch <- c.acceptedDesc
	ch <- c.servedDesc
	ch <- c.authorizedDesc
	ch <- c.requestsDesc
	ch <- c.requestsSuccessDesc
	ch <- c.sessionUpDesc
	ch <- c.sessionStreamsDesc
	ch <- c.sessionStreamsStartedDesc
	ch <- c.sessionStreamsSucceededDesc
	ch <- c.sessionStreamsFailedDesc
	ch <- c.sessionBytesSentDesc
	ch <- c.sessionBytesReceivedDesc
	ch <- c.sessionWireLengthSentDesc
	ch <- c.sessionWireLengthReceivedDesc
}

func (c *serverMetricsCollector) Collect(ch chan<- prometheus.Metric) {
	now := time.Now()
	stats := c.s.Stats()

	ch <- prometheus.MustNewConstMetric(c.uptimeDesc, prometheus.GaugeValue,
		now.Sub(c.s.started).Seconds())
	ch <- prometheus.MustNewConstMetric(c.sessionsDesc, prometheus.GaugeValue,
		float64(stats.NumSessions))
	ch <- prometheus.MustNewConstMetric(c.rxBytesDesc, prometheus.CounterValue,
		float64(stats.Rx))
	ch <- prometheus.MustNewConstMetric(c.txBytesDesc, prometheus.CounterValue,
		float64(stats.Tx))
	ch <- prometheus.MustNewConstMetric(c.acceptedDesc, prometheus.CounterValue,
		float64(stats.Accepted))
	ch <- prometheus.MustNewConstMetric(c.servedDesc, prometheus.CounterValue,
		float64(stats.Served))
	ch <- prometheus.MustNewConstMetric(c.authorizedDesc, prometheus.CounterValue,
		float64(stats.Authorized))
	ch <- prometheus.MustNewConstMetric(c.requestsDesc, prometheus.CounterValue,
		float64(stats.ReqTotal))
	ch <- prometheus.MustNewConstMetric(c.requestsSuccessDesc, prometheus.CounterValue,
		float64(stats.ReqSuccess))

	for _, ss := range stats.sessions {
		up := 0.0
		if ss.Active {
			up = 1.0
		}
		ch <- prometheus.MustNewConstMetric(c.sessionUpDesc, prometheus.GaugeValue,
			up, ss.Name)
		ch <- prometheus.MustNewConstMetric(c.sessionStreamsStartedDesc, prometheus.CounterValue,
			float64(ss.StreamsStarted), ss.Name)
		ch <- prometheus.MustNewConstMetric(c.sessionStreamsSucceededDesc, prometheus.CounterValue,
			float64(ss.StreamsSucceeded), ss.Name)
		ch <- prometheus.MustNewConstMetric(c.sessionStreamsFailedDesc, prometheus.CounterValue,
			float64(ss.StreamsFailed), ss.Name)
		ch <- prometheus.MustNewConstMetric(c.sessionBytesSentDesc, prometheus.CounterValue,
			float64(ss.BytesSent), ss.Name)
		ch <- prometheus.MustNewConstMetric(c.sessionBytesReceivedDesc, prometheus.CounterValue,
			float64(ss.BytesReceived), ss.Name)
		ch <- prometheus.MustNewConstMetric(c.sessionWireLengthSentDesc, prometheus.CounterValue,
			float64(ss.WireLengthSent), ss.Name)
		ch <- prometheus.MustNewConstMetric(c.sessionWireLengthReceivedDesc, prometheus.CounterValue,
			float64(ss.WireLengthReceived), ss.Name)
	}
}

func newAPIMetricsHandler(s *Server) http.Handler {
	registry := prometheus.NewRegistry()
	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		newServerMetricsCollector(s),
	)
	handler := promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		handler.ServeHTTP(w, r)
	})
}

// RunHTTPServer serves health, config, stats, metrics, gc, and stack endpoints.
func RunHTTPServer(l net.Listener, s *Server) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthy", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/config", &apiConfigHandler{s: s})
	mux.Handle("/stats", &apiStatsHandler{s: s})
	mux.Handle("/metrics", newAPIMetricsHandler(s))
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
