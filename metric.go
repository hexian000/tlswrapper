package tlswrapper

import (
	"encoding/json"
	"fmt"
	"math"
	"math/bits"
	"net"
	"net/http"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/hexian000/tlswrapper/slog"
)

var uptime = time.Now()

var iec_units = [...]string{
	"B", "KiB", "MiB", "GiB", "TiB", "PiB", "EiB", "ZiB", "YiB",
}

func intlog2(value uint64) int {
	return 63 - bits.LeadingZeros64(value)
}

func formatIEC(value uint64) string {
	if value < 8192 {
		return fmt.Sprintf("%d %s", value, iec_units[0])
	}
	n := (intlog2(value) - 3) / 10
	if n >= len(iec_units) {
		n = len(iec_units) - 1
	}
	v := math.Ldexp(float64(value), n*-10)
	if v < 10.0 {
		return fmt.Sprintf("%.02f %s", v, iec_units[n])
	}
	if v < 100.0 {
		return fmt.Sprintf("%.01f %s", v, iec_units[n])
	}
	return fmt.Sprintf("%.0f %s", v, iec_units[n])
}

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
		printf("%-20s: %s", "Heap Used", formatIEC(memstats.HeapAlloc))
		printf("%-20s: %s", "Next GC", formatIEC(memstats.NextGC))
		printf("%-20s: %s", "Heap Allocated", formatIEC(memstats.HeapSys))
		printf("%-20s: %s", "Stack Used", formatIEC(memstats.StackInuse))
		printf("%-20s: %s", "Stack Allocated", formatIEC(memstats.StackSys))
		printf("%-20s: %s", "Total Allocated", formatIEC(memstats.Sys))
		if memstats.LastGC > 0 {
			printf("%-20s: %v ago", "Last GC", time.Since(time.Unix(0, int64(memstats.LastGC))))
			printf("%-20s: %v", "Last GC pause", time.Duration(memstats.PauseNs[(memstats.NumGC+255)%256]))
		}
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
