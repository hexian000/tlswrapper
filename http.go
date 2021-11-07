package main

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"time"

	"github.com/hexian000/tlswrapper/proxy"
	"github.com/hexian000/tlswrapper/slog"
)

func (s *Server) serveHTTP(l net.Listener, config *ServerConfig) {
	defer s.wg.Done()
	defer func() {
		_ = l.Close()
	}()
	server := &http.Server{
		Handler: s.newHandler(config),
	}
	_ = server.Serve(l)
}

const configHost = "config.tlswrapper.lan"

func newBanner() string {
	return fmt.Sprintf("%s\nServer Time: %v\n\n", banner, time.Now())
}

type proxyHandler struct {
	s *Server
}

func (h *proxyHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	dialed, err := dialer.DialContext(req.Context(), network, req.Host)
	if err != nil {
		slog.Verbose("http:", err)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(newBanner() + err.Error() + "\n"))
		return
	}
	w.WriteHeader(http.StatusOK)
	accepted, err := proxy.Hijack(w)
	if err != nil {
		slog.Warning("http:", err)
		return
	}
	h.s.forward(accepted, dialed)
}

func (s *Server) appendConfigHandler(mux *http.ServeMux) {
	mux.HandleFunc(configHost+"/status", func(w http.ResponseWriter, req *http.Request) {
		start := time.Now()
		w.WriteHeader(http.StatusOK)
		buf := bufio.NewWriter(w)
		defer func() {
			_ = buf.Flush()
		}()
		_, _ = buf.WriteString(newBanner())
		runtime.GC()
		var memstats runtime.MemStats
		runtime.ReadMemStats(&memstats)
		_, _ = buf.WriteString(fmt.Sprintln("Num CPU:", runtime.NumCPU()))
		_, _ = buf.WriteString(fmt.Sprintln("Num Goroutines:", runtime.NumGoroutine()))
		_, _ = buf.WriteString(fmt.Sprintln("Heap Used:", memstats.Alloc))
		_, _ = buf.WriteString(fmt.Sprintln("Heap Allocated:", memstats.Sys))
		_, _ = buf.WriteString(fmt.Sprintln("Stack Used:", memstats.StackInuse))
		_, _ = buf.WriteString(fmt.Sprintln("Stack Allocated:", memstats.StackSys))
		_, _ = buf.WriteString("\n=== Sessions ===\n\n")
		var readTotal, writeTotal uint64
		func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			for name, info := range s.sessions {
				r, w := info.count()
				readTotal += r
				writeTotal += w
				_, _ = buf.WriteString(fmt.Sprintf(
					"%s\n  Last Seen: %v\n  Traffic I/O(bytes): %d / %d\n\n",
					name,
					info.lastSeen,
					r, w,
				))
			}
		}()
		_, _ = buf.WriteString(fmt.Sprintf(
			"Total\n  Traffic I/O(bytes): %d / %d\n\n",
			readTotal, writeTotal,
		))
		var stack [262144]byte
		n := runtime.Stack(stack[:], true)
		_, _ = buf.WriteString("\n=== Stack ===\n\n")
		_, _ = buf.Write(stack[:n])
		_, _ = buf.WriteString("\n==========\n")
		_, _ = buf.WriteString(fmt.Sprintln("Generated in", time.Since(start)))
	})
}

func (s *Server) newHandler(config *ServerConfig) http.Handler {
	h := &proxyHandler{s}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		host := req.URL.Hostname()
		if host == configHost {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if req.URL.Port() == "" {
			host += ":80"
		}
		dialed, err := dialer.DialContext(req.Context(), network, host)
		if err != nil {
			slog.Verbose("http:", err)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(newBanner() + err.Error() + "\n"))
			return
		}
		accepted, err := proxy.Hijack(w)
		if err != nil {
			_ = dialed.Close()
			slog.Warning("http:", err)
			return
		}
		err = req.Write(dialed)
		if err != nil {
			_ = accepted.Close()
			_ = dialed.Close()
			slog.Verbose("http:", err)
			return
		}
		h.s.forward(accepted, dialed)
	})
	if !config.DisableWebConfig {
		s.appendConfigHandler(mux)
	}
	return &proxy.Handler{
		Connect: h,
		Default: mux,
	}
}
