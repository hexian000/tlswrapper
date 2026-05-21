package h2mux

import "testing"

func TestConfigWindowOptions(t *testing.T) {
	tests := []struct {
		name       string
		cfg        Config
		wantDial   int
		wantServer int
	}{
		{name: "dynamic", cfg: Config{}, wantDial: 4, wantServer: 4},
		{name: "session-only", cfg: Config{SessionWindow: 256 * 1024}, wantDial: 5, wantServer: 5},
		{name: "stream-only", cfg: Config{StreamWindow: 256 * 1024}, wantDial: 5, wantServer: 5},
		{name: "both", cfg: Config{SessionWindow: 256 * 1024, StreamWindow: 512 * 1024}, wantDial: 6, wantServer: 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := len(tt.cfg.grpcDialOptions()); got != tt.wantDial {
				t.Fatalf("grpcDialOptions() = %d, want %d", got, tt.wantDial)
			}
			if got := len(tt.cfg.grpcServerOptions()); got != tt.wantServer {
				t.Fatalf("grpcServerOptions() = %d, want %d", got, tt.wantServer)
			}
		})
	}
}

func TestConfigIgnoresTooSmallWindows(t *testing.T) {
	cfg := &Config{SessionWindow: 32 * 1024, StreamWindow: 48 * 1024}
	if got := cfg.sessionWindow(); got != 0 {
		t.Fatalf("sessionWindow() = %d, want 0", got)
	}
	if got := cfg.streamWindow(); got != 0 {
		t.Fatalf("streamWindow() = %d, want 0", got)
	}
}
