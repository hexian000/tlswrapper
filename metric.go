package tlswrapper

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/hexian000/tlswrapper/formats"
	"github.com/hexian000/tlswrapper/slog"
)

var uptime = time.Now()

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
	fprintf(w, "%-20s: %s / %s\n", "Heap In-use",
		formats.IECBytes(float64(memstats.HeapInuse)),
		formats.IECBytes(float64(memstats.HeapSys-memstats.HeapReleased)))
	fprintf(w, "%-20s: %s / %s\n", "Stack In-use",
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
		Served    uint64
		Rejected  uint64
		Success   uint64
		Rx, Tx    uint64
		Timestamp time.Time
	}{Timestamp: time.Time{}}

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
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if stateless {
			w.Header().Set("Cache-Control", "no-store")
		}
		w.WriteHeader(http.StatusOK)
		fprintf(w, "tlswrapper %s\n  %s\n\n", Version, Homepage)
		fprintf(w, "%-20s: %v\n", "Server Time", now.Format(time.RFC3339))
		fprintf(w, "%-20s: %s\n", "Uptime", formats.Duration(now.Sub(uptime)))
		fprintf(w, "%-20s: %v\n", "Max Procs", runtime.GOMAXPROCS(-1))
		fprintf(w, "%-20s: %v\n", "Num Goroutines", runtime.NumGoroutine())
		printMemStats(w, true)
		fprintf(w, "%-20s: %v / %v\n", "Sessions / Streams", s.NumSessions(), s.NumStreams())
		rx, tx := s.CountBytes()
		fprintf(w, "%-20s: %s / %s\n", "Traffic (Rx/Tx)", formats.IECBytes(float64(rx)), formats.IECBytes(float64(tx)))
		accepted, served := s.CountAccepts()
		rejected := accepted - served
		fprintf(w, "%-20s: %d (%d rejected)\n", "Listener Accepts", served, rejected)
		authorized := s.CountAuthorized()
		fprintf(w, "%-20s: %d (%+d)\n", "Authorized Conns", authorized, served-authorized)
		requests, success := s.CountRequests()
		fprintf(w, "%-20s: %d (%+d)\n", "Requests", success, requests-success)

		if !stateless {
			dt := now.Sub(last.Timestamp).Seconds()

			fprintf(w, "%-20s: %.1f/s (%.1f/s rejected)\n", "Incoming Conns",
				float64(served-last.Served)/dt, float64(rejected-last.Rejected)/dt)
			fprintf(w, "%-20s: %.1f/s\n", "Request Success", float64(success-last.Success)/dt)
			fprintf(w, "%-20s: %s/s / %s/s\n", "Bandwidth (Rx/Tx)",
				formats.IECBytes(float64(rx-last.Rx)/dt), formats.IECBytes(float64(tx-last.Tx)/dt))

			last.Served, last.Rejected = accepted, rejected
			last.Success = success
			last.Rx, last.Tx = rx, tx
			last.Timestamp = now
		}

		fprintf(w, "\n==============================\n")
		fprintf(w, "runtime: %s\n", runtime.Version())
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
		start := time.Now()
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		if stateless {
			w.Header().Set("Cache-Control", "no-store")
		}
		w.WriteHeader(http.StatusOK)
		var buf [65536]byte
		n := runtime.Stack(buf[:], true)
		fprintf(w, "%s\n", string(buf[:n]))
		fprintf(w, "\n==============================\n")
		fprintf(w, "generated in %s\n", formats.Duration(time.Since(start)))
		fprintf(w, "runtime: %s\n", runtime.Version())
	})
	server := &http.Server{
		Handler:  mux,
		ErrorLog: slog.Wrap(slog.Default(), slog.LevelError),
	}
	return server.Serve(l)
}
