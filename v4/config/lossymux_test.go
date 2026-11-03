// tlswrapper (c) 2021-2026 He Xian <hexian000@outlook.com>
// This code is licensed under MIT license (see LICENSE for details)

package config

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestLossyMuxConfig(t *testing.T) {
	for _, input := range []string{`{}`, `{"mux":{"lossymux":{}}}`, `{"mux":{"lossymux":{"future":true}}}`} {
		cfg, err := Load([]byte(input))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.Mux.LossyMux != (LossyMux{IdleTimeout: 60, Queue: 64}) {
			t.Fatalf("defaults: %+v", cfg.Mux.LossyMux)
		}
	}
	for _, values := range []string{`"enabled":true,"idle_timeout":1,"queue":1`, `"enabled":false,"idle_timeout":86400,"queue":65535`} {
		cfg, err := Load([]byte(`{"mux":{"lossymux":{` + values + `}}}`))
		if err != nil {
			t.Fatal(err)
		}
		cloned, err := cfg.Clone()
		if err != nil {
			t.Fatal(err)
		}
		if cloned.Mux.LossyMux != cfg.Mux.LossyMux {
			t.Fatal("clone changed extension")
		}
		data, err := json.Marshal(cloned)
		if err != nil {
			t.Fatal(err)
		}
		again, err := Load(data)
		if err != nil || again.Mux.LossyMux != cfg.Mux.LossyMux {
			t.Fatalf("round trip: %v", err)
		}
	}
	for _, value := range []string{`null`, `true`, `[]`, `{"enabled":null}`, `{"enabled":1}`, `{"enabled":"true"}`, `{"queue":0}`, `{"queue":65536}`, `{"queue":-1}`, `{"queue":1.5}`, `{"queue":null}`, `{"idle_timeout":0}`, `{"idle_timeout":86401}`, `{"idle_timeout":"60"}`} {
		if _, err := Load([]byte(`{"mux":{"lossymux":` + value + `}}`)); err == nil {
			t.Fatalf("accepted %s", value)
		}
	}
	if _, err := Load([]byte(`{"type":"application/x-multiplexd-config; version=1"}`)); err == nil || !strings.Contains(err.Error(), "type") {
		t.Fatal("changed config MIME compatibility")
	}
}
