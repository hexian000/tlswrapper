package tlswrapper

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"runtime/debug"
	"sort"
	"time"

	"github.com/hexian000/tlswrapper/v2/formats"
	"github.com/hexian000/tlswrapper/v2/slog"
)

func fprintf(w io.Writer, format string, v ...interface{}) {
	_, err := w.Write([]byte(fmt.Sprintf(format, v...)))
	if err != nil {
		panic(err)
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

func RunHTTPServer(l net.Listener, s *Server) error {
	last := struct {
		Accepted   uint64
		Served     uint64
		ReqTotal   uint64
		ReqSuccess uint64
		Rx, Tx     uint64
		Timestamp  time.Time
	}{}

	mux := http.NewServeMux()
	mux.HandleFunc("/healthy", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		b, err := json.MarshalIndent(s.getConfig(), "", "    ")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
	})
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		var stateless bool
		if r.Method == http.MethodGet {
			stateless = true
		} else if r.Method == http.MethodPost {
			stateless = false
		} else {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		now := time.Now()
		uptime := now.Sub(s.started)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if stateless {
			w.Header().Set("Cache-Control", "no-store")
		}
		w.WriteHeader(http.StatusOK)
		fprintf(w, "tlswrapper %s\n  %s\n\n", Version, Homepage)
		fprintf(w, "%-20s: %s\n", "Server Time", now.Format(time.RFC3339))
		fprintf(w, "%-20s: %s\n", "Uptime", formats.Duration(uptime))
		fprintf(w, "%-20s: %v\n", "Max Procs", runtime.GOMAXPROCS(-1))
		fprintf(w, "%-20s: %v\n", "Num Goroutines", runtime.NumGoroutine())
		printMemStats(w, true)
		stats := s.Stats()
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
		fprintf(w, "%-20s: %d (%+d)\n", "Requests",
			stats.ReqSuccess, stats.ReqTotal-stats.ReqSuccess)

		if !stateless {
			dt := now.Sub(last.Timestamp).Seconds()

			fprintf(w, "%-20s: %.1f/s (%+.1f/s rejected)\n", "Authorized",
				float64(stats.Served-last.Served)/dt,
				float64((stats.Accepted-stats.Served)-(last.Accepted-last.Served))/dt)
			fprintf(w, "%-20s: %.1f/s (%+.1f/s)\n", "Requests",
				float64(stats.ReqSuccess-last.ReqSuccess)/dt,
				float64((stats.ReqTotal-stats.ReqSuccess)-(last.ReqTotal-last.ReqSuccess))/dt)
			fprintf(w, "%-20s: Rx %s/s, Tx %s/s\n", "Bandwidth",
				formats.IECBytes(float64(stats.Rx-last.Rx)/dt),
				formats.IECBytes(float64(stats.Tx-last.Tx)/dt))

			last.Accepted, last.Served = stats.Accepted, stats.Served
			last.ReqTotal, last.ReqSuccess = stats.ReqTotal, stats.ReqSuccess
			last.Rx, last.Tx = stats.Rx, stats.Tx
			last.Timestamp = now
		}

		fprintf(w, "\n> Tunnels\n")
		sort.Slice(stats.tunnels, func(i, j int) bool {
			return stats.tunnels[i].Name < stats.tunnels[j].Name
		})
		for _, t := range stats.tunnels {
			if t.NumSessions > 0 {
				fprintf(w, "%-20q: %d (%d streams), online since %s\n", t.Name, t.NumSessions, t.NumStreams, t.LastChanged.Format(time.RFC3339))
			} else if (t.LastChanged != time.Time{}) {
				fprintf(w, "%-20q: offline since %s\n", t.Name, t.LastChanged.Format(time.RFC3339))
			} else {
				fprintf(w, "%-20q: never seen\n", t.Name)
			}
		}
	})
	mux.HandleFunc("/gc", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		start := time.Now()
		debug.FreeOSMemory()
		printMemStats(w, false)
		fprintf(w, "%-20s: %s\n", "Time Cost", formats.Duration(time.Since(start)))
	})
	mux.HandleFunc("/stack", func(w http.ResponseWriter, r *http.Request) {
		var stateless bool
		if r.Method == http.MethodGet {
			stateless = true
		} else if r.Method == http.MethodPost {
			stateless = false
		} else {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if stateless {
			w.Header().Set("Cache-Control", "no-store")
		}
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
