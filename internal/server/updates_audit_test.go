package server

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

func TestManualUpdatesRejectReservedAutomaticCheckBeforeAcceptance(t *testing.T) {
	s, h := testServer(t)
	m, err := update.New(config.UpdateConfig{Enabled: true}, t.TempDir(), update.BuildInfo{Version: "1.4.2"})
	if err != nil {
		t.Fatal(err)
	}
	s.SetUpdateManager(m)
	s.ConfigureUpdates(context.Background(), true, 2)
	cookie, csrf := loginForTest(t, h)
	release, ok := m.TryReserveOperation()
	if !ok {
		t.Fatal("could not reserve automatic check")
	}
	defer release()
	for _, action := range []string{"check", "download", "apply", "rollback"} {
		w := request(t, h, "POST", "/api/v1/admin/updates/"+action, nil, cookie, csrf)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "update_busy") || s.updateBusy.Load() || m.Snapshot().State != "idle" {
			t.Fatalf("reserved %s was accepted: %d %s", action, w.Code, w.Body.String())
		}
	}
	release()
	// Invalid manual requests must release the reservation without starting work.
	w := request(t, h, "POST", "/api/v1/admin/updates/apply", nil, cookie, csrf)
	if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "update_not_ready") {
		t.Fatalf("expected not-ready: %d %s", w.Code, w.Body.String())
	}
	releaseAgain, ok := m.TryReserveOperation()
	if !ok {
		t.Fatal("validation failure leaked reservation")
	}
	releaseAgain()
}

func TestContainerUpdatesExposeVersionAndRejectInstallationImmediately(t *testing.T) {
	s, h := testServer(t)
	m, err := update.New(config.UpdateConfig{Enabled: true, InstallMode: "container"}, t.TempDir(), update.BuildInfo{Version: "1.4.2"})
	if err != nil {
		t.Fatal(err)
	}
	s.SetUpdateManager(m)
	s.ConfigureUpdates(context.Background(), true, 2)
	cookie, csrf := loginForTest(t, h)
	w := request(t, h, "GET", "/api/v1/admin/updates/status", nil, cookie, "")
	for _, want := range []string{`"version":"1.4.2"`, `"enabled":true`, `"install_enabled":false`, `"install_mode":"container"`} {
		if w.Code != 200 || !strings.Contains(w.Body.String(), want) {
			t.Fatalf("missing %s: %d %s", want, w.Code, w.Body.String())
		}
	}
	for _, action := range []string{"download", "apply", "rollback"} {
		w := request(t, h, "POST", "/api/v1/admin/updates/"+action, nil, cookie, csrf)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "update_container_managed") || s.updateBusy.Load() || m.Snapshot().State != "idle" {
			t.Fatalf("container %s: %d %s", action, w.Code, w.Body.String())
		}
	}
}
