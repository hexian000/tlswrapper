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
		b, err := json.MarshalIndent(s.getConfig(), "", "    ")
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(err.Error()))
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
			_, err := w.Write([]byte(fmt.Sprintf(format, v...)))
			if err != nil {
				panic(err)
			}
		}
		printf("tlswrapper %s\n  %s\n\n", Version, Homepage)
		printf("%-20s: %v\n", "Server Time", now.Format(time.RFC3339))
		printf("%-20s: %s\n", "Uptime", formats.Duration(now.Sub(uptime)))
		printf("%-20s: %v\n", "Max Procs", runtime.GOMAXPROCS(-1))
		printf("%-20s: %v\n", "Num Goroutines", runtime.NumGoroutine())
		var memstats runtime.MemStats
		runtime.ReadMemStats(&memstats)
		printf("%-20s: %s\n", "Heap Used", formats.IECBytes(float64(memstats.HeapAlloc)))
		printf("%-20s: %s\n", "Next GC", formats.IECBytes(float64(memstats.NextGC)))
		printf("%-20s: %s\n", "Heap Allocated", formats.IECBytes(float64(memstats.HeapSys-memstats.HeapReleased)))
		printf("%-20s: %s\n", "Stack Used", formats.IECBytes(float64(memstats.StackInuse)))
		printf("%-20s: %s\n", "Stack Allocated", formats.IECBytes(float64(memstats.StackSys)))
		printf("%-20s: %s\n", "Total Allocated", formats.IECBytes(float64(memstats.Sys-memstats.HeapReleased)))
		if memstats.LastGC > 0 {
			printf("%-20s: %s ago\n", "Last GC", formats.Duration(time.Since(time.Unix(0, int64(memstats.LastGC)))))
			printf("%-20s: %s\n", "Last GC pause", formats.Duration(time.Duration(memstats.PauseNs[(memstats.NumGC+255)%256])))
		} else {
			printf("%-20s: %s\n", "Last GC", "(never)")
			printf("%-20s: %s\n", "Last GC pause", "(never)")
		}
		printf("\n")
		printf("%-20s: %v\n", "Tunnels", len(s.getConfig().Tunnels))
		printf("%-20s: %v\n", "Active Sessions", s.NumSessions())
		printf("%-20s: %v\n", "Active Streams", s.f.Count())
		printf("%-20s: %v\n", "Managed Routines", s.g.Count())
		rx, tx := s.CountBytes()
		printf("%-20s: %s / %s\n", "Traffic (Rx/Tx)", formats.IECBytes(float64(rx)), formats.IECBytes(float64(tx)))
		accepted, refused := s.CountAccepts()
		printf("%-20s: %d accepted / %d refused\n", "Flood Protect", accepted, refused)
		printf("\n==============================\n")
		printf("runtime: %s\n", runtime.Version())
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
			_, err := w.Write([]byte(fmt.Sprintf(format, v...)))
			if err != nil {
				panic(err)
			}
		}
		start := time.Now()
		debug.FreeOSMemory()
		var memstats runtime.MemStats
		runtime.ReadMemStats(&memstats)
		printf("%-20s: %s\n", "Live Heap", formats.IECBytes(float64(memstats.HeapSys-memstats.HeapReleased)))
		printf("%-20s: %s\n", "Live Stack", formats.IECBytes(float64(memstats.StackSys)))
		printf("%-20s: %s\n", "Allocated", formats.IECBytes(float64(memstats.Sys-memstats.HeapReleased)))
		printf("%-20s: %s\n", "Time Cost", formats.Duration(time.Since(start)))
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
			_, err := w.Write([]byte(fmt.Sprintf(format, v...)))
			if err != nil {
				panic(err)
			}
		}
		var buf [65536]byte
		n := runtime.Stack(buf[:], true)
		printf("%s\n", string(buf[:n]))
		printf("\n==============================\n")
		printf("generated in %s\n", formats.Duration(time.Since(start)))
		printf("runtime: %s\n", runtime.Version())
	})
	return http.Serve(l, mux)
}
