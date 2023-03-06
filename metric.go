package tlswrapper

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"time"

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
		printf("%-20s: %v", "Uptime", now.Sub(uptime))
		printf("%-20s: %v", "Max Procs", runtime.GOMAXPROCS(-1))
		printf("%-20s: %v", "Num Goroutines", runtime.NumGoroutine())
		var memstats runtime.MemStats
		runtime.ReadMemStats(&memstats)
		printf("%-20s: %d KiB", "Heap Used", memstats.Alloc>>10)
		printf("%-20s: %d KiB", "Heap Allocated", memstats.Sys>>10)
		printf("%-20s: %d KiB", "Stack Used", memstats.StackInuse>>10)
		printf("%-20s: %d KiB", "Stack Allocated", memstats.StackSys>>10)
		printf("%-20s: %v ago", "Last GC", now.Sub(time.Unix(0, int64(memstats.LastGC))))
		printf("%-20s: %v", "Last GC pause", time.Duration(memstats.PauseNs[(memstats.NumGC+255)%256]))
		printf("")
		printf("%-20s: %v", "Tunnels", len(s.c.Tunnels))
		printf("%-20s: %v", "Active Sessions", s.NumSessions())
		printf("%-20s: %v", "Active Streams", s.f.Count())
		printf("%-20s: %v", "Managed Routines", s.g.Count())
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
		start := time.Now()
		runtime.GC()
		_, err := w.Write([]byte(time.Since(start).String() + "\n"))
		if err != nil {
			panic(err)
		}
	})
	mux.HandleFunc("/stack", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		start := time.Now()
		w.WriteHeader(http.StatusOK)
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
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
