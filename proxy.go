package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
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
		Handler: newHandler(s, s.Proxy.DisableApi),
	}
	_ = server.Serve(l)
}

func (s *Server) routedDial(host string) (c *clientSession, ok bool) {
	if c, ok := s.dials[host]; ok {
		return c, true
	}
	host = s.Proxy.DefaultRoute
	if host == "" {
		return nil, true
	}
	if c, ok := s.dials[host]; ok {
		return c, true
	}
	return nil, false
}

type mainHandler struct {
	*Server
	localhost string
	mux       *http.ServeMux
}

func (mainHandler) newBanner() string {
	return fmt.Sprintf("%s\nserver time: %v\n\n", banner, time.Now())
}

func (h *mainHandler) Error(w http.ResponseWriter, err error, code int) {
	http.Error(w, h.newBanner()+err.Error(), code)
}

func (h *mainHandler) proxyError(w http.ResponseWriter, err error) {
	slog.Verbose("http:", err)
	if err, ok := err.(net.Error); ok && err.Timeout() {
		h.Error(w, err, http.StatusGatewayTimeout)
	} else {
		h.Error(w, err, http.StatusBadGateway)
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

func appendHostToXForwardHeader(header http.Header, host string) {
	if prior, ok := header["X-Forwarded-For"]; ok {
		host = strings.Join(prior, ", ") + ", " + host
	}
	header.Set("X-Forwarded-For", host)
}

func (h *mainHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	if req.Method == http.MethodConnect {
		h.ServeConnect(w, req)
		return
	}
	if req.URL.Scheme != "http" {
		http.Error(w, "Unsupported protocol scheme "+req.URL.Scheme, http.StatusBadRequest)
		return
	}
	if req.Host == h.localhost {
		if h.mux != nil {
			h.mux.ServeHTTP(w, req)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
		return
	}

	client := &http.Client{
		Timeout: time.Duration(h.ConnectTimeout) * time.Second,
	}
	req.RequestURI = ""
	delHopHeaders(req.Header)

	if clientIP, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
		appendHostToXForwardHeader(req.Header, clientIP)
	}

	resp, err := client.Do(req)
	if err != nil {
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
	io.Copy(w, resp.Body)
}

func (h *mainHandler) ServeConnect(w http.ResponseWriter, req *http.Request) {
	dialed, err := dialer.DialContext(req.Context(), network, req.Host)
	if err != nil {
		h.proxyError(w, err)
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

func newHandler(s *Server, disableApi bool) *mainHandler {
	h := &mainHandler{
		Server:    s,
		localhost: s.Config.Proxy.HostName,
	}
	if !disableApi {
		h.mux = http.NewServeMux()
		h.mux.HandleFunc(h.localhost+"/status", h.handleStatus)
	}
	return h
}
