package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"strings"
	"time"

	"github.com/hexian000/tlswrapper/proxy"
	"github.com/hexian000/tlswrapper/slog"
)

const apiDomain = ".tlswrapper.api"

func (s *Server) serveHTTP(l net.Listener) {
	defer func() {
		_ = l.Close()
	}()
	server := &http.Server{
		Handler:           newHandler(s, &s.cfg.Proxy),
		ReadHeaderTimeout: s.cfg.Timeout(),
	}
	_ = server.Serve(l)
}

func (s *Server) routedDial(ctx context.Context, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	route := s.cfg.Proxy.findRoute(host)
	if route == "" {
		return s.dialDirect(ctx, addr)
	}
	if c, ok := s.dials[route]; ok {
		slog.Verbose("route: forward", addr, "via", route)
		return c.dialMux(ctx)
	}
	return nil, fmt.Errorf("no route to host: %v", host)
}

func (s *Server) routedProxyDial(ctx context.Context, addr string) (net.Conn, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	route := s.cfg.Proxy.findRoute(host)
	if route == "" {
		return s.dialDirect(ctx, addr)
	}
	if c, ok := s.dials[route]; ok {
		slog.Verbose("route: proxy", addr, "via", route)
		return c.proxyDial(ctx, addr)
	}
	return nil, fmt.Errorf("no route to host: %v", host)
}

type HTTPHandler struct {
	*Server
	config *ProxyConfig
	mux    *http.ServeMux
}

func (h *HTTPHandler) newBanner() string {
	return fmt.Sprintf(
		"tlswrapper@%s %s\n  %s\n\nserver time: %v\n\n",
		h.config.LocalHost,
		version, homepage,
		time.Now().Format(time.RFC3339),
	)
}

func (h *HTTPHandler) Error(w http.ResponseWriter, err error, code int) {
	http.Error(w, h.newBanner()+fmt.Sprintf("%v\n", err), code)
}

func (h *HTTPHandler) proxyError(w http.ResponseWriter, err error) {
	slog.Verbose("http:", err)
	if err, ok := err.(net.Error); ok && err.Timeout() {
		h.Error(w, err, http.StatusGatewayTimeout)
	} else {
		h.Error(w, err, http.StatusBadGateway)
	}
}

func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodConnect {
		h.ServeConnect(w, req)
		return
	}
	if req.URL.Scheme != "http" {
		http.Error(w, "Unsupported protocol scheme: "+req.URL.String(), http.StatusBadRequest)
		return
	}
	if strings.EqualFold(req.URL.Hostname(), h.config.LocalHost+apiDomain) {
		if h.mux != nil {
			h.mux.ServeHTTP(w, req)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
		return
	}

	ctx := h.newContext()
	defer h.deleteContext(ctx)
	addr := req.URL.Host
	if req.URL.Port() == "" {
		addr += ":80"
	}
	client := &http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return h.routedDial(ctx, addr)
			},
			DisableKeepAlives: true,
		},
		Timeout: h.cfg.Timeout(),
	}
	resp, err := client.Do(req)
	if err != nil {
		slog.Verbose("http:", err)
		h.proxyError(w, err)
		return
	}
	w.WriteHeader(resp.StatusCode)
	err = resp.Write(w)
	if err != nil {
		slog.Verbose("http:", err)
	}
}

func (h *HTTPHandler) ServeConnect(w http.ResponseWriter, req *http.Request) {
	_, _, err := net.SplitHostPort(req.Host)
	if err != nil {
		slog.Warning("proxy connect:", err)
		h.Error(w, err, http.StatusBadRequest)
		return
	}
	ctx := h.newContext()
	defer h.deleteContext(ctx)
	dialed, err := h.routedProxyDial(ctx, req.Host)
	if err != nil {
		slog.Error("proxy dial:", err)
		h.proxyError(w, err)
		return
	}
	w.WriteHeader(http.StatusOK)
	accepted, err := proxy.Hijack(w)
	if err != nil {
		slog.Error("hijack:", err)
		return
	}
	h.forward(accepted, dialed)
}

func (h *HTTPHandler) handleStatus(respWriter http.ResponseWriter, req *http.Request) {
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
		for name, session := range h.sessions {
			r, w := session.meter.Count()
			n := session.mux.NumStreams()
			idleSince := "now"
			if n == 0 {
				idleSince = time.Since(session.lastSeen).String()
			}
			_, _ = buf.WriteString(fmt.Sprintf(
				"%s\n  Num Streams: %d\n  Age: %v (since %v)\n  Idle since: %v\n  Traffic I/O(bytes): %d / %d\n\n",
				name, n, time.Since(session.created), session.created, idleSince, r, w,
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

func newHandler(s *Server, config *ProxyConfig) *HTTPHandler {
	h := &HTTPHandler{
		Server: s,
		config: config,
	}
	if !config.DisableAPI {
		host := h.config.LocalHost + apiDomain
		h.mux = http.NewServeMux()
		h.mux.HandleFunc(host+"/status", h.handleStatus)
	}
	return h
}
