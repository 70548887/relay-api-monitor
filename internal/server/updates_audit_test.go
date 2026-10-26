package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"relaylab/internal/config"
	"relaylab/internal/update"
)

func TestUpdatesRequireAuthenticationAndCSRF(t *testing.T) {
	s, h := testServer(t)
	m, err := update.New(config.UpdateConfig{Enabled: false}, t.TempDir(), update.BuildInfo{Version: "1.2.2"})
	if err != nil {
		t.Fatal(err)
	}
	s.SetUpdateManager(m)
	for _, path := range []string{"/admin/updates", "/admin/updates/status", "/admin/updates/history"} {
		if w := request(t, h, "GET", "/api/v1"+path, nil, nil, ""); w.Code != 401 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	cookie, csrf := loginForTest(t, h)
	for _, action := range []string{"check", "download", "apply", "rollback"} {
		path := "/api/v1/admin/updates/" + action
		if w := request(t, h, "POST", path, nil, cookie, ""); w.Code != 403 {
			t.Fatalf("csrf %s: %d", action, w.Code)
		}
		if w := request(t, h, "POST", path, nil, cookie, csrf); w.Code != 501 {
			t.Fatalf("disabled %s: %d", action, w.Code)
		}
	}
	if w := request(t, h, "GET", "/api/v1/admin/updates", nil, cookie, ""); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
}

func TestMaintenanceBlocksAuthenticationAndAllBusinessWrites(t *testing.T) {
	s, h := testServer(t)
	cookie, csrf := loginForTest(t, h)
	fence := filepath.Join(t.TempDir(), "maintenance.request")
	s.SetMaintenanceFile(fence)
	leave, ok := s.gate.Enter()
	if !ok {
		t.Fatal("admission")
	}
	defer leave()
	if err := os.WriteFile(fence, []byte("operation"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/test/models", "/test/generate", "/auth/login", "/setup", "/admin/jobs", "/admin/platforms"} {
		w := request(t, h, http.MethodPost, "/api/v1"+path, map[string]any{}, cookie, csrf)
		if w.Code != 503 {
			t.Fatalf("%s: %d", path, w.Code)
		}
	}
	if paused, active, _ := s.RuntimeState(); !paused || active != 1 {
		t.Fatalf("paused=%v active=%d", paused, active)
	}
	if w := request(t, h, "GET", "/api/v1/admin/platforms", nil, cookie, ""); w.Code != 200 {
		t.Fatal(w.Code)
	}
	leave()
	if _, active, _ := s.RuntimeState(); active != 0 {
		t.Fatal("inflight count not released")
	}
}

func TestUpdatesDoNotApplyThroughLegacyLauncher(t *testing.T) {
	s, h := testServer(t)
	m, err := update.New(config.UpdateConfig{Enabled: true}, t.TempDir(), update.BuildInfo{Version: "1.2.2"})
	if err != nil {
		t.Fatal(err)
	}
	s.SetUpdateManager(m)
	s.ConfigureUpdates(context.Background(), false, 1)
	cookie, csrf := loginForTest(t, h)
	for _, action := range []string{"apply", "rollback"} {
		if w := request(t, h, "POST", "/api/v1/admin/updates/"+action, nil, cookie, csrf); w.Code != 409 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
}
