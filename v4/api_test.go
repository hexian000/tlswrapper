package tlswrapper

import (
	"bytes"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hexian000/tlswrapper/v4/config"
)

func TestAPIConfigHandler(t *testing.T) {
	t.Run("method-not-allowed", func(t *testing.T) {
		h := &apiConfigHandler{s: newTestServer(t, nil)}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/config", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
		}
	})

	t.Run("request-too-large", func(t *testing.T) {
		h := &apiConfigHandler{s: newTestServer(t, nil)}
		req := httptest.NewRequest(http.MethodPost, "/config", strings.NewReader("{}"))
		req.ContentLength = maxContentLength + 1
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusRequestEntityTooLarge)
		}
	})

	t.Run("invalid-body", func(t *testing.T) {
		h := &apiConfigHandler{s: newTestServer(t, nil)}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/config", strings.NewReader("{bad json")))
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusBadRequest)
		}
	})

	t.Run("load-config-tls-error-continues", func(t *testing.T) {
		s := newTestServer(t, nil)
		h := &apiConfigHandler{s: s}
		// Bad TLS cert: reload logs the error but still applies the config.
		body := []byte(`{"type":"` + config.Type + `","tls":{"cert":"bad cert","key":"bad key"}}`)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/config", bytes.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d (TLS error should be logged, not abort reload)", rec.Code, http.StatusOK)
		}
	})

	t.Run("success", func(t *testing.T) {
		s := newTestServer(t, nil)
		h := &apiConfigHandler{s: s}
		body := []byte(`{"type":"` + config.Type + `","timeout":30}`)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/config", bytes.NewReader(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		cfg, _ := s.getConfig()
		if cfg.PingTimeout != 30 {
			t.Fatalf("PingTimeout = %d, want 30", cfg.PingTimeout)
		}
	})
}

func TestAPIStatsHandler(t *testing.T) {
	s := newTestServer(t, nil)
	s.started = time.Now().Add(-2 * time.Minute)
	s.stats.authorized.Store(3)
	s.stats.request.Store(5)
	s.stats.success.Store(4)
	s.recentEvents.Add(time.Now(), "config loaded")
	h := &apiStatsHandler{
		s: s,
		last: apiStats{
			Accepted:   1,
			Served:     1,
			ReqTotal:   2,
			ReqSuccess: 1,
			Timestamp:  time.Now().Add(-1 * time.Second),
		},
	}

	t.Run("get", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/stats?nobanner=true&eventlog=1", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("Cache-Control = %q, want %q", got, "no-store")
		}
		body := rec.Body.String()
		for _, want := range []string{"Num Sessions", "Requests", "Recent Events", "config loaded"} {
			if !strings.Contains(body, want) {
				t.Fatalf("body %q does not contain %q", body, want)
			}
		}
	})

	t.Run("post", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/stats?nobanner=true", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		body := rec.Body.String()
		for _, want := range []string{"Authorized", "Bandwidth"} {
			if !strings.Contains(body, want) {
				t.Fatalf("body %q does not contain %q", body, want)
			}
		}
		if h.last.Timestamp.IsZero() {
			t.Fatal("expected last timestamp to be updated")
		}
	})

	t.Run("method-not-allowed", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/stats", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
		}
	})
}

func TestAPIMetricsHandler(t *testing.T) {
	s := newTestServer(t, nil)
	s.started = time.Now().Add(-1 * time.Minute)
	h := newAPIMetricsHandler(s)

	t.Run("get", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		body := rec.Body.String()
		for _, want := range []string{"tlswrapper_uptime_seconds", "tlswrapper_sessions"} {
			if !strings.Contains(body, want) {
				t.Fatalf("body %q does not contain %q", body, want)
			}
		}
		if got := rec.Header().Get("Cache-Control"); got != "no-store" {
			t.Fatalf("Cache-Control = %q, want %q", got, "no-store")
		}
	})

	t.Run("method-not-allowed", func(t *testing.T) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/metrics", nil))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
		}
	})
}

func TestRunHTTPServer(t *testing.T) {
	s := newTestServer(t, nil)
	s.started = time.Now().Add(-1 * time.Minute)
	l, err := net.Listen("tcp", freePort(t))
	if err != nil {
		t.Fatal(err)
	}
	errCh := make(chan error, 1)
	go func() {
		errCh <- RunHTTPServer(l, s)
	}()
	t.Cleanup(func() {
		_ = l.Close()
		err := <-errCh
		if err != nil && !errors.Is(err, net.ErrClosed) && !strings.Contains(err.Error(), "closed network connection") {
			t.Fatalf("RunHTTPServer: %v", err)
		}
	})

	client := &http.Client{Timeout: 5 * time.Second}
	baseURL := "http://" + l.Addr().String()

	assertStatus := func(t *testing.T, method, path string, want int) string {
		t.Helper()
		req, err := http.NewRequest(method, baseURL+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != want {
			t.Fatalf("%s %s: status = %d, want %d", method, path, resp.StatusCode, want)
		}
		return string(body)
	}

	assertStatus(t, http.MethodGet, "/healthy", http.StatusOK)
	assertStatus(t, http.MethodGet, "/stats?nobanner=true", http.StatusOK)
	assertStatus(t, http.MethodGet, "/metrics", http.StatusOK)
	assertStatus(t, http.MethodGet, "/gc", http.StatusMethodNotAllowed)
	body := assertStatus(t, http.MethodPost, "/gc", http.StatusOK)
	if !strings.Contains(body, "Time Cost") {
		t.Fatalf("body %q does not contain %q", body, "Time Cost")
	}
	body = assertStatus(t, http.MethodGet, "/stack", http.StatusOK)
	if !strings.Contains(body, "goroutine") {
		t.Fatalf("body %q does not contain %q", body, "goroutine")
	}
}
