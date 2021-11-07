package main

import (
	"net/http"
	"runtime"

	"github.com/hashicorp/yamux"
	"github.com/hexian000/tlswrapper/proxy"
	"github.com/hexian000/tlswrapper/slog"
)

func (s *Server) serveHTTP(session *yamux.Session, config *ServerConfig) {
	defer s.wg.Done()
	defer func() {
		_ = session.Close()
	}()
	server := &http.Server{
		Handler: s.newHandler(),
	}
	_ = server.Serve(session)
}

const configHost = "config.tlswrapper.lan"

type proxyHandler struct {
	s *Server
}

func (h *proxyHandler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	dialed, err := dialer.DialContext(req.Context(), network, req.Host)
	if err != nil {
		slog.Verbose("http:", err)
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(err.Error()))
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

func (s *Server) newHandler() http.Handler {
	h := &proxyHandler{s}
	client := http.Client{}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		clientReq, err := http.NewRequest(req.Method, req.URL.String(), req.Body)
		if err != nil {
			slog.Verbose("http:", err)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		resp, err := client.Do(clientReq)
		if err != nil {
			slog.Verbose("http:", err)
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(err.Error()))
			return
		}
		err = resp.Write(w)
		if err != nil {
			slog.Verbose("http:", err)
		}
	})
	mux.HandleFunc(configHost+"/stack", func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
		var buf [65536]byte
		n := runtime.Stack(buf[:], true)
		_, err := w.Write(buf[:n])
		if err != nil {
			slog.Warning("http:", err)
			return
		}
	})
	return &proxy.Handler{
		Connect: h,
		Default: mux,
	}
}
