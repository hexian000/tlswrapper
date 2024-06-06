package tlswrapper

import (
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"runtime/debug"
	"sort"
	"strconv"
	"time"

	"github.com/hexian000/gosnippets/formats"
	"github.com/hexian000/gosnippets/slog"
)

const maxContentLength = 4194304

func setRespHeader(h http.Header, mimeType string, nocache bool) {
	h.Set("Content-Type", mime.FormatMediaType(mimeType, map[string]string{"charset": "utf-8"}))
	h.Set("X-Content-Type-Options", "nosniff")
	if nocache {
		h.Set("Cache-Control", "no-store")
	}
}

func fprintf(w io.Writer, format string, v ...interface{}) {
	_, err := w.Write([]byte(fmt.Sprintf(format, v...)))
	if err != nil {
		panic(err)
	}
}

type apiConfigHandler struct {
	s *Server
}

func (h *apiConfigHandler) Post(w http.ResponseWriter, r *http.Request) {
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
	cfg, err := parseConfig(b[:n])
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(formats.Error(err)))
		return
	}
	err = h.s.LoadConfig(cfg)
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(formats.Error(err)))
		return
	}
}

func (h *apiConfigHandler) Get(w http.ResponseWriter, r *http.Request) {
	b, err := json.MarshalIndent(h.s.getConfig(), "", "    ")
	if err != nil {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(formats.Error(err)))
		return
	}
	setRespHeader(w.Header(), "application/json", true)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}

func (h *apiConfigHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.Get(w, r)
	case http.MethodPost:
		h.Post(w, r)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func printMemStats(w io.Writer, lastGC bool) {
	var memstats runtime.MemStats
	runtime.ReadMemStats(&memstats)
	fprintf(w, "%-20s: %s ≤ %s\n", "Heap Next GC",
		formats.IECBytes(float64(memstats.HeapAlloc)),
		formats.IECBytes(float64(memstats.NextGC)))
	fprintf(w, "%-20s: %s ≤ %s\n", "Heap In-use",
		formats.IECBytes(float64(memstats.HeapInuse)),
		formats.IECBytes(float64(memstats.HeapSys-memstats.HeapReleased)))
	fprintf(w, "%-20s: %s ≤ %s\n", "Stack In-use",
		formats.IECBytes(float64(memstats.StackInuse)),
		formats.IECBytes(float64(memstats.StackSys)))
	runtimeSys := memstats.MSpanSys + memstats.MCacheSys + memstats.BuckHashSys + memstats.GCSys + memstats.OtherSys
	fprintf(w, "%-20s: %s\n", "Runtime Allocated", formats.IECBytes(float64(runtimeSys)))
	fprintf(w, "%-20s: %s (+%s)\n", "Total Allocated",
		formats.IECBytes(float64(memstats.Sys-memstats.HeapReleased)),
		formats.IECBytes(float64(memstats.HeapReleased)))
	fprintf(w, "%-20s: %.07f%%\n", "GC CPU Fraction",
		memstats.GCCPUFraction*100.0/float64(runtime.GOMAXPROCS(0)))
	if !lastGC {
		return
	}
	if memstats.LastGC > 0 {
		lastGC := time.Since(time.Unix(0, int64(memstats.LastGC)))
		fprintf(w, "%-20s: %s ago\n", "Last GC", formats.Duration(lastGC))
		lastPause := time.Duration(memstats.PauseNs[(memstats.NumGC+255)%256])
		fprintf(w, "%-20s: %s\n", "Last GC pause", formats.Duration(lastPause))
	} else {
		fprintf(w, "%-20s: %s\n", "Last GC", "(never)")
		fprintf(w, "%-20s: %s\n", "Last GC pause", "(never)")
	}
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

func (h *apiStatsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var stateless bool
	if r.Method == http.MethodGet {
		stateless = true
	} else if r.Method == http.MethodPost {
		stateless = false
	} else {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	uri, err := url.ParseRequestURI(r.RequestURI)
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		slog.Debugf("uri: %s", formats.Error(err))
		return
	}
	query := uri.Query()
	rt := false
	if s := query.Get("runtime"); s != "" {
		if b, err := strconv.ParseBool(s); err == nil {
			rt = b
		}
	}

	now := time.Now()
	uptime := now.Sub(h.s.started)
	setRespHeader(w.Header(), "text/plain", stateless)
	w.WriteHeader(http.StatusOK)
	fprintf(w, "tlswrapper %s\n  %s\n\n", Version, Homepage)
	fprintf(w, "%-20s: %s\n", "Server Time", now.Format(time.RFC3339))
	fprintf(w, "%-20s: %s\n", "Uptime", formats.Duration(uptime))
	if rt {
		fprintf(w, "%-20s: %v\n", "Max Procs", runtime.GOMAXPROCS(-1))
		fprintf(w, "%-20s: %v\n", "Num Goroutines", runtime.NumGoroutine())
		printMemStats(w, true)
	}
	stats := h.s.Stats()
	fprintf(w, "%-20s: %d (%d streams)\n", "Num Sessions", stats.NumSessions, stats.NumStreams)
	fprintf(w, "%-20s: Rx %s, Tx %s\n", "Traffic",
		formats.IECBytes(float64(stats.Rx)), formats.IECBytes(float64(stats.Tx)))
	uptimeHrs := uptime.Hours()
	fprintf(w, "%-20s: Rx %s/hrs, Tx %s/hrs\n", "Avg Bandwidth",
		formats.IECBytes(float64(stats.Rx)/uptimeHrs),
		formats.IECBytes(float64(stats.Tx)/uptimeHrs))
	rejected := stats.Accepted - stats.Served
	fprintf(w, "%-20s: %d (%+d rejected)\n", "Listener Accepts",
		stats.Served, rejected)
	fprintf(w, "%-20s: %d (%+d)\n", "Authorizations",
		stats.Authorized, stats.Served-stats.Authorized)
	fprintf(w, "%-20s: %d (%+d)\n", "Stream Requests",
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
			fprintf(w, "%-20q: %s, since %s\n", t.Name, s, t.LastChanged.Format(time.RFC3339))
		} else {
			fprintf(w, "%-20q: never seen\n", t.Name)
		}
	}

	fprintf(w, "\n> Recent Events\n")
	if err := h.s.recentEvents.Format(w); err != nil {
		panic(err)
	}
}

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
		printMemStats(w, false)
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
