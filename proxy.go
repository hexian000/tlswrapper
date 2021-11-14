package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/hexian000/tlswrapper/proxy"
	"github.com/hexian000/tlswrapper/slog"
)

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
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	route, newHost := s.cfg.Proxy.FindRoute(host)
	dialAddr := net.JoinHostPort(newHost, port)
	if route == "" {
		return s.dialDirect(ctx, dialAddr)
	}
	if c, ok := s.dials[route]; ok {
		slog.Verbose("route: forward", addr, "to", route, dialAddr)
		if dialAddr == "" {
			return c.dialMux(ctx)
		}
		return c.proxyDial(ctx, dialAddr)
	}
	return nil, fmt.Errorf("no route to address: %v", addr)
}

type HTTPHandler struct {
	*Server
	config *ProxyConfig
	mux    *http.ServeMux
	client *http.Client
}

func (h *HTTPHandler) newBanner() string {
	return fmt.Sprintf(
		"tlswrapper@%s %s\n  %s\n\nserver time: %v\n\n",
		h.config.LocalHost,
		version, homepage,
		time.Now().Format(time.RFC3339),
	)
}

func (h *HTTPHandler) Error(w http.ResponseWriter, msg string, code int) {
	http.Error(w, h.newBanner()+msg, code)
}

func (h *HTTPHandler) proxyError(w http.ResponseWriter, err error) {
	slog.Verbose("http:", err)
	msg := fmt.Sprintf("%v", err)
	if err, ok := err.(net.Error); ok && err.Timeout() {
		h.Error(w, msg, http.StatusGatewayTimeout)
	} else {
		h.Error(w, msg, http.StatusBadGateway)
	}
}

var hopHeaders = [...]string{
	"Connection",
	"Keep-Alive",
	"Proxy-Authenticate",
	"Proxy-Authorization",
	"Te",
	"Trailers",
	"Transfer-Encoding",
	"Upgrade",
}

func delHopHeaders(header http.Header) {
	for _, h := range hopHeaders {
		header.Del(h)
	}
}

func (h *HTTPHandler) proxy(w http.ResponseWriter, req *http.Request) {
	ctx := h.newContext()
	defer h.deleteContext(ctx)
	req.RequestURI = ""
	delHopHeaders(req.Header)
	if host, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
		if prior, ok := req.Header["X-Forwarded-For"]; ok {
			host = strings.Join(prior, ", ") + ", " + host
		}
		req.Header.Set("X-Forwarded-For", host)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		slog.Verbose("http:", err)
		h.proxyError(w, err)
		return
	}
	defer func() {
		_ = resp.Body.Close()
	}()

	delHopHeaders(resp.Header)
	for k, v := range resp.Header {
		for _, i := range v {
			w.Header().Add(k, i)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

const apiHost = "api.tlswrapper"

func (h *HTTPHandler) isAPIHost(hostname string) bool {
	if apiHostName, ok := h.config.MakeFQDN(apiHost); ok {
		return strings.EqualFold(hostname, apiHostName)
	}
	return false
}

func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodConnect {
		h.ServeConnect(w, req)
		return
	}
	if req.URL.Host == "" || h.isAPIHost(req.URL.Hostname()) {
		if h.mux != nil {
			h.mux.ServeHTTP(w, req)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
		return
	}
	h.proxy(w, req)
}

func (h *HTTPHandler) ServeConnect(w http.ResponseWriter, req *http.Request) {
	_, _, err := net.SplitHostPort(req.Host)
	if err != nil {
		msg := fmt.Sprintf("proxy connect: %v", err)
		slog.Warning(msg)
		h.Error(w, msg, http.StatusBadRequest)
		return
	}
	ctx := h.newContext()
	defer h.deleteContext(ctx)
	dialed, err := h.routedDial(ctx, req.Host)
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

func (h *HTTPHandler) handleCluster(respWriter http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		h.Error(respWriter, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
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
	_, _ = w.WriteString(fmt.Sprintf("localhost: %s\n", h.config.LocalHost))
	for name, c := range h.Server.dials {
		_, _ = w.WriteString(fmt.Sprintf("%s: %v\n", name, c.session.mux.RemoteAddr()))
	}
	_, _ = w.WriteString("\n==========\n")
	_, _ = w.WriteString(fmt.Sprintln("Generated in", time.Since(start)))
}

var (
	statusPattern = regexp.MustCompile(`^/status/([\w\.]+)$`)
)

func (h *HTTPHandler) handleStatus(respWriter http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet {
		h.Error(respWriter, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
		return
	}
	matches := statusPattern.FindStringSubmatch(req.URL.Path)
	if matches != nil {
		// TODO
		h.Error(respWriter, http.StatusText(http.StatusForbidden), http.StatusForbidden)
		return
	}

	// self status
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
				"%s\n  Num Streams: %d\n  Age: %v (since %v)\n  Idle: %v\n  Traffic I/O(bytes): %d / %d\n\n",
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
	h.client = &http.Client{
		Transport: &http.Transport{
			Proxy: func(r *http.Request) (*url.URL, error) {
				return r.URL, nil
			},
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				return h.routedDial(ctx, addr)
			},
		},
		Timeout: h.cfg.Timeout(),
	}
	if !config.DisableAPI {
		h.mux = http.NewServeMux()
		h.mux.HandleFunc("/cluster", h.handleCluster)
		h.mux.HandleFunc("/status/", h.handleStatus)
	}
	return h
}
