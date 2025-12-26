// tlswrapper (c) 2021-2025 He Xian <hexian000@outlook.com>
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
	"github.com/hexian000/tlswrapper/v3/config"
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
	_, err := w.Write([]byte(fmt.Sprintf(format, v...)))
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

	fprintf(w, "\n> Tunnels\n")
	sort.Slice(stats.tunnels, func(i, j int) bool {
		return stats.tunnels[i].Name < stats.tunnels[j].Name
	})
	for _, t := range stats.tunnels {
		if (t.LastChanged != time.Time{}) {
			s := "offline"
			if t.NumSessions > 0 {
				s = fmt.Sprintf("%d (%d streams)", t.NumSessions, t.NumStreams)
			}
			fprintf(w, "%-20q: %s %s\n", t.Name, t.LastChanged.Format(slog.TimeLayout), s)
		} else {
			fprintf(w, "%-20q: never seen\n", t.Name)
		}
	}

	if evlog > 0 {
		fprintf(w, "\n> Recent Events\n")
		if err := h.s.recentEvents.Format(w, evlog); err != nil {
			panic(err)
		}
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
