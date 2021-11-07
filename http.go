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
		Handler: newHandler(s, config),
	}
	_ = server.Serve(l)
}

const configHost = "config.tlswrapper.lan"

type mainHandler struct {
	*Server
	mux *http.ServeMux
}

func (mainHandler) newBanner() string {
	return fmt.Sprintf("%s\nserver time: %v\n\n", banner, time.Now())
}

func (h *mainHandler) Error(w http.ResponseWriter, err error, code int) {
	http.Error(w, h.newBanner()+err.Error(), code)
}

func (h *mainHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodConnect {
		h.ServeConnect(w, req)
		return
	}
	if req.Host == configHost {
		if h.mux != nil {
			h.mux.ServeHTTP(w, req)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
		return
	}
	host := req.URL.Hostname()
	if req.URL.Port() == "" {
		host += ":80"
	}
	dialed, err := dialer.DialContext(req.Context(), network, host)
	if err != nil {
		slog.Verbose("http:", err)
		if err, ok := err.(net.Error); ok && err.Timeout() {
			h.Error(w, err, http.StatusGatewayTimeout)
		} else {
			h.Error(w, err, http.StatusBadGateway)
		}
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
	h.forward(accepted, dialed)
}

func (h *mainHandler) ServeConnect(w http.ResponseWriter, req *http.Request) {
	dialed, err := dialer.DialContext(req.Context(), network, req.Host)
	if err != nil {
		slog.Verbose("http:", err)
		if err, ok := err.(net.Error); ok && err.Timeout() {
			h.Error(w, err, http.StatusGatewayTimeout)
		} else {
			h.Error(w, err, http.StatusBadGateway)
		}
		return
	}
	w.WriteHeader(http.StatusOK)
	accepted, err := proxy.Hijack(w)
	if err != nil {
		slog.Warning("http:", err)
		return
	}
	h.forward(accepted, dialed)
}

func (h *mainHandler) handleStatus(respWriter http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		respWriter.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	start := time.Now()
	respWriter.Header().Set("Content-Type", "text/plain; charset=utf-8")
	respWriter.Header().Set("X-Content-Type-Options", "nosniff")
	w := bufio.NewWriter(respWriter)
	defer func() {
		_ = w.Flush()
	}()
	_, _ = w.WriteString(h.newBanner())
	runtime.GC()
	var memstats runtime.MemStats
	runtime.ReadMemStats(&memstats)
	_, _ = w.WriteString(fmt.Sprintf("Uptime: %v (since %v)\n", time.Since(h.startTime), h.startTime))
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
		h.mu.Lock()
		defer h.mu.Unlock()
		for name, info := range h.sessions {
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

func newHandler(s *Server, config *ServerConfig) *mainHandler {
	h := &mainHandler{Server: s}
	if !config.DisableWebConfig {
		h.mux = http.NewServeMux()
		h.mux.HandleFunc(configHost+"/status", h.handleStatus)
	}
	return h
}
