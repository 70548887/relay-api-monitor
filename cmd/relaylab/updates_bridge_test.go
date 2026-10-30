package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"relaylab/internal/config"
	"relaylab/internal/update"
)

func TestLauncherDetectionPreservesDisabledBinaryAndSkipsContainer(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		enabled    bool
		managed    bool
	}{
		{"binary enabled", "binary", true, true},
		{"binary explicitly disabled", "binary", false, true},
		{"container", "container", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			requests := make(chan struct{}, 2)
			token := strings.Repeat("x", 40)
			launcher := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Path != "/status" || r.Header.Get("X-RelayLab-Launcher-Token") != token {
					t.Errorf("unexpected launcher request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				requests <- struct{}{}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"protocol":2}`))
			}))
			defer launcher.Close()
			t.Setenv("RELAYLAB_LAUNCHER_INSTANCE", "fixture")
			t.Setenv("RELAYLAB_LAUNCHER_CONTROL_ADDR", strings.TrimPrefix(launcher.URL, "http://"))
			t.Setenv("RELAYLAB_LAUNCHER_TOKEN", token)
			manager, err := update.New(config.UpdateConfig{Enabled: tc.enabled, InstallMode: tc.mode}, t.TempDir(), update.BuildInfo{Version: "1.4.2"})
			if err != nil {
				t.Fatal(err)
			}
			managed, protocol := wireLauncherUpdater(manager, true)
			if managed != tc.managed || (tc.managed && protocol != 2) || (len(requests) > 0) != tc.managed {
				t.Fatalf("managed=%v protocol=%d requests=%d", managed, protocol, len(requests))
			}
		})
	}
}
