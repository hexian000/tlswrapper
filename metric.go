package tlswrapper

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/hexian000/tlswrapper/formats"
	"github.com/hexian000/tlswrapper/slog"
)

var uptime = time.Now()

func RunHTTPServer(l net.Listener, s *Server) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthy", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		b, err := json.MarshalIndent(s.c, "", "    ")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(err.Error()))
			return
		}
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(b)
	})
	mux.HandleFunc("/stats", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		now := time.Now()
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		defer func() {
			if err := recover(); err != nil {
				slog.Error(err)
				return
			}
		}()
		printf := func(format string, v ...interface{}) {
			_, err := w.Write([]byte(fmt.Sprintf(format, v...) + "\n"))
			if err != nil {
				panic(err)
			}
		}
		printf("tlswrapper %s\n  %s\n", Version, Homepage)
		printf("%-20s: %v", "Server Time", now.Format(time.RFC3339))
		printf("%-20s: %v", "Uptime", formats.DurationSeconds(now.Sub(uptime)))
		printf("%-20s: %v", "Max Procs", runtime.GOMAXPROCS(-1))
		printf("%-20s: %v", "Num Goroutines", runtime.NumGoroutine())
		var memstats runtime.MemStats
		runtime.ReadMemStats(&memstats)
		printf("%-20s: %s", "Heap Used", formats.IECBytes(float64(memstats.HeapAlloc)))
		printf("%-20s: %s", "Next GC", formats.IECBytes(float64(memstats.NextGC)))
		printf("%-20s: %s", "Heap Allocated", formats.IECBytes(float64(memstats.HeapSys-memstats.HeapReleased)))
		printf("%-20s: %s", "Stack Used", formats.IECBytes(float64(memstats.StackInuse)))
		printf("%-20s: %s", "Stack Allocated", formats.IECBytes(float64(memstats.StackSys)))
		printf("%-20s: %s", "Total Allocated", formats.IECBytes(float64(memstats.Sys-memstats.HeapReleased)))
		if memstats.LastGC > 0 {
			printf("%-20s: %v ago", "Last GC", time.Since(time.Unix(0, int64(memstats.LastGC))))
			printf("%-20s: %v", "Last GC pause", time.Duration(memstats.PauseNs[(memstats.NumGC+255)%256]))
		}
		printf("")
		printf("%-20s: %v", "Tunnels", len(s.c.Tunnels))
		printf("%-20s: %v", "Active Sessions", s.NumSessions())
		printf("%-20s: %v", "Active Streams", s.f.Count())
		printf("%-20s: %v", "Managed Routines", s.g.Count())
		rx, tx := s.CountBytes()
		printf("%-20s: %s / %s", "Traffic (Rx/Tx)", formats.IECBytes(float64(rx)), formats.IECBytes(float64(tx)))
		printf("")
		printf("==============================")
		printf("runtime: %s", runtime.Version())
	})
	mux.HandleFunc("/gc", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		printf := func(format string, v ...interface{}) {
			_, err := w.Write([]byte(fmt.Sprintf(format, v...) + "\n"))
			if err != nil {
				panic(err)
			}
		}
		start := time.Now()
		debug.FreeOSMemory()
		printf("%v", time.Since(start))
	})
	mux.HandleFunc("/stack", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		start := time.Now()
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.WriteHeader(http.StatusOK)
		defer func() {
			if err := recover(); err != nil {
				slog.Error(err)
				return
			}
		}()
		printf := func(format string, v ...interface{}) {
			_, err := w.Write([]byte(fmt.Sprintf(format, v...) + "\n"))
			if err != nil {
				panic(err)
			}
		}
		var buf [65536]byte
		n := runtime.Stack(buf[:], true)
		printf(string(buf[:n]))
		printf("")
		printf("==============================")
		printf("generated in %v", time.Since(start))
		printf("runtime: %s", runtime.Version())
	})
	return http.Serve(l, mux)
}
