package tlswrapper

import (
	"fmt"
	"net"
	"net/http"
	"runtime"
	"time"

	"github.com/hexian000/tlswrapper/slog"
)

func RunHTTPServer(l net.Listener, s *Server) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthy", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/stat", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		start := time.Now()
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
		printf("%-20s: %v", "Time", time.Now().Format(time.RFC3339))
		printf("%-20s: %v", "Num CPU", runtime.NumCPU())
		printf("%-20s: %v", "Num Goroutines", runtime.NumGoroutine())
		var memstats runtime.MemStats
		runtime.ReadMemStats(&memstats)
		printf("%-20s: %v", "Heap Used", memstats.Alloc)
		printf("%-20s: %v", "Heap Allocated", memstats.Sys)
		printf("%-20s: %v", "Stack Used", memstats.StackInuse)
		printf("%-20s: %v", "Stack Allocated", memstats.StackSys)
		printf("")
		printf("%-20s: %v", "Tunnels", len(s.c.Tunnels))
		printf("%-20s: %v", "Active Sessions", s.NumSessions())
		printf("%-20s: %v", "Active Streams", s.f.Count())
		printf("")
		printf("==============================")
		printf("generated in %v", time.Since(start))
		printf("runtime: %s", runtime.Version())
	})
	return http.Serve(l, mux)
}
