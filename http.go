package main

import (
	"bufio"
	"bytes"
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

func (s *Server) newBanner() string {
	return fmt.Sprintf("%s\nserver time: %v\n\n", banner, time.Now())
}

type proxyHandler struct {
	s *Server
}

func (h *proxyHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	dialed, err := dialer.DialContext(req.Context(), network, req.Host)
	if err != nil {
		slog.Verbose("http:", err)
		if err, ok := err.(net.Error); ok && err.Timeout() {
			w.WriteHeader(http.StatusGatewayTimeout)
		} else {
			w.WriteHeader(http.StatusBadGateway)
		}
		_, _ = w.Write([]byte(h.s.newBanner() + err.Error() + "\n"))
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

type serverHandlers struct {
	*Server
}

func (s *serverHandlers) ServeHTTP(w http.ResponseWriter, req *http.Request) {
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
		if err, ok := err.(net.Error); ok && err.Timeout() {
			w.WriteHeader(http.StatusGatewayTimeout)
		} else {
			w.WriteHeader(http.StatusBadGateway)
		}
		_, _ = w.Write([]byte(s.newBanner() + err.Error() + "\n"))
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
	s.forward(accepted, dialed)
}

func (s *serverHandlers) handleStatus(respWriter http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		respWriter.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	start := time.Now()
	respWriter.WriteHeader(http.StatusOK)
	w := bufio.NewWriter(respWriter)
	defer func() {
		_ = w.Flush()
	}()
	_, _ = w.WriteString(s.newBanner())
	runtime.GC()
	var memstats runtime.MemStats
	runtime.ReadMemStats(&memstats)
	_, _ = w.WriteString(fmt.Sprintf("Uptime: %v (since %v)\n", time.Since(s.startTime), s.startTime))
	_, _ = w.WriteString(fmt.Sprintln("Num CPU:", runtime.NumCPU()))
	_, _ = w.WriteString(fmt.Sprintln("Num Goroutines:", runtime.NumGoroutine()))
	_, _ = w.WriteString(fmt.Sprintln("Heap Used:", memstats.Alloc))
	_, _ = w.WriteString(fmt.Sprintln("Heap Allocated:", memstats.Sys))
	_, _ = w.WriteString(fmt.Sprintln("Stack Used:", memstats.StackInuse))
	_, _ = w.WriteString(fmt.Sprintln("Stack Allocated:", memstats.StackSys))
	_, _ = w.WriteString("\n=== Sessions ===\n\n")
	var numSessions, numStreams int
	_, _ = w.Write(func() []byte {
		buf := &bytes.Buffer{}
		s.mu.Lock()
		defer s.mu.Unlock()
		for name, info := range s.sessions {
			r, w := info.count()
			n := info.session.NumStreams()
			idleSince := "now"
			if n == 0 {
				idleSince = time.Since(info.lastSeen).String()
			}
			_, _ = buf.WriteString(fmt.Sprintf(
				"%s\n  Num Streams: %d\n  Age: %v (since %v)\n  Idle since: %v\n  Traffic I/O(bytes): %d / %d\n\n",
				name, n, time.Since(info.created), info.created, idleSince, r, w,
			))
			numStreams += n
			numSessions++
		}
		return buf.Bytes()
	}())
	_, _ = w.WriteString(fmt.Sprintf(
		"Total\n  Num Sessions: %d\n  Num Streams: %d\n\n",
		numSessions, numStreams,
	))
	var stack [262144]byte
	n := runtime.Stack(stack[:], true)
	_, _ = w.WriteString("\n=== Stack ===\n\n")
	_, _ = w.Write(stack[:n])
	_, _ = w.WriteString("\n==========\n")
	_, _ = w.WriteString(fmt.Sprintln("Generated in", time.Since(start)))
}

func (h *serverHandlers) register(mux *http.ServeMux) {
	mux.HandleFunc(configHost+"/status", h.handleStatus)
}

func (s *Server) newHandler(config *ServerConfig) http.Handler {
	h := &serverHandlers{s}
	mux := http.NewServeMux()
	mux.Handle("/", h)
	if !config.DisableWebConfig {
		h.register(mux)
	}
	return &proxy.Handler{
		Connect: &proxyHandler{s},
		Default: mux,
	}
}
