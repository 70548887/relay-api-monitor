package update

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"relaylab/internal/config"
)

func TestSemverAndAssetSelection(t *testing.T) {
	if !isNewer("v1.2.0", "v1.1.9") || isNewer("v1.1.0", "v1.1.0") || !isNewer("v1.0.0", "dev") {
		t.Fatal("semantic version comparison failed")
	}
	name := "relaylab-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	if got, ok := selectAsset([]Asset{{Name: "relaylab-linux-amd64"}, {Name: name}}, runtime.GOOS, runtime.GOARCH); !ok || got.Name != name {
		t.Fatalf("asset selection failed: %#v %v", got, ok)
	}
}

func TestCheckDownloadAndJournal(t *testing.T) {
	payload := []byte("relaylab update payload")
	name := "relaylab-" + runtime.GOOS + "-" + runtime.GOARCH
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/o/r/releases/latest":
			_ = json.NewEncoder(w).Encode(map[string]any{"tag_name": "v9.9.9", "name": "test", "html_url": "https://github.com/o/r/releases/tag/v9.9.9", "assets": []map[string]any{{"name": name, "size": len(payload), "browser_download_url": server.URL + "/download/" + name}, {"name": "manifest.json", "size": 0, "browser_download_url": server.URL + "/manifest.json"}}})
		case "/manifest.json":
			hash := sha256.Sum256(payload)
			_ = json.NewEncoder(w).Encode(map[string]any{"assets": []map[string]any{{"name": name, "sha256": fmt.Sprintf("%x", hash[:])}}})
		case "/download/" + name:
			w.Write(payload)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	d := t.TempDir()
	cfg := config.UpdateConfig{Enabled: true, GitHubOwner: "o", GitHubRepo: "r"}
	m, err := New(cfg, d, BuildInfo{Version: "v1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	m.apiBase = server.URL
	snap, err := m.Check(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.State != "available" || snap.Latest == nil {
		t.Fatalf("unexpected check: %#v", snap)
	}
	snap, err = m.Download(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.State != "ready" || snap.StagedPath == "" {
		t.Fatalf("unexpected download: %#v", snap)
	}
	if snap.Latest == nil || snap.Latest.SHA256 == "" {
		t.Fatal("download did not record SHA-256")
	}
	if got, err := os.ReadFile(snap.StagedPath); err != nil || string(got) != string(payload) {
		t.Fatalf("staged payload mismatch: %v", err)
	}
	if _, err = os.Stat(filepath.Join(d, "update-state.json")); err != nil {
		t.Fatal(err)
	}
}

func TestApplyWithoutLauncherLeavesPendingRestart(t *testing.T) {
	d := t.TempDir()
	m, err := New(config.UpdateConfig{Enabled: true, GitHubOwner: "o", GitHubRepo: "r"}, d, BuildInfo{Version: "v1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	m.state.Latest = &UpdateInfo{CurrentVersion: "v1.0.0", LatestVersion: "v2.0.0", Available: true}
	m.state.StagedPath = filepath.Join(d, "relaylab-new")
	snap, err := m.Apply(context.Background())
	if err != ErrApplyUnavailable || snap.State != "pending_restart" {
		t.Fatalf("unexpected apply result: %#v %v", snap, err)
	}
}

func TestApplyWithLauncherKeepsValidationJournal(t *testing.T) {
	d := t.TempDir()
	m, err := New(config.UpdateConfig{Enabled: true, GitHubOwner: "o", GitHubRepo: "r"}, d, BuildInfo{Version: "v1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(d, "updates", "relaylab-new")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	m.state.Latest = &UpdateInfo{CurrentVersion: "v1.0.0", LatestVersion: "v2.0.0", Available: true}
	m.state.StagedPath = path
	m.SetApplier(func(context.Context, string, UpdateInfo) error { return nil })
	snap, err := m.Apply(context.Background())
	if err != nil || snap.State != "validating" || snap.Operation == nil {
		t.Fatalf("unexpected apply result: %#v %v", snap, err)
	}
}
