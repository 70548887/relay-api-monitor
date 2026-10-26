package update

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDownloadAfterRestartRefreshesSavedTagURLs(t *testing.T) {
	for _, private := range []bool{false, true} {
		name := "public"
		if private {
			name = "private"
		}
		t.Run(name, func(t *testing.T) {
			f, m := newFixture(t)
			if private {
				m.cfg.GitHubToken = "restart-fixture-token"
			}
			for i := range f.release.Assets {
				f.release.Assets[i].BrowserURL += "?signature=public-fixture-signature"
				f.release.Assets[i].APIURL += "?signature=private-fixture-signature"
			}
			if _, err := m.Check(context.Background()); err != nil {
				t.Fatal(err)
			}
			reloaded, err := New(m.cfg, m.dataDir, m.build)
			if err != nil {
				t.Fatal(err)
			}
			reloaded.apiBase = f.server.URL
			// A newer or unavailable latest release must not change the saved
			// target. Recovery has to use the exact original tag endpoint.
			f.failPath["/repos/o/r/releases/latest"] = http.StatusInternalServerError
			snap, err := reloaded.Download(context.Background())
			if err != nil || snap.State != "ready" || snap.Latest.LatestVersion != "v1.2.2" || !snap.Release.Assets[0].Verified {
				t.Fatalf("recovered download failed: %#v %v", snap, err)
			}
			if f.requests["/repos/o/r/releases/latest"] != 1 || f.requests["/repos/o/r/releases/tags/v1.2.2"] != 1 {
				t.Fatalf("recovery did not use the saved tag: %#v", f.requests)
			}
			manifestPath, downloadPath, wantAuth := "/manifest.json", "/download/core", ""
			if private {
				manifestPath, downloadPath, wantAuth = "/api/manifest", "/api/core", "Bearer restart-fixture-token"
			}
			for _, path := range []string{"/repos/o/r/releases/tags/v1.2.2", manifestPath, downloadPath} {
				if f.headers[path] != wantAuth {
					t.Fatalf("incorrect recovered asset authorization at %s", path)
				}
			}
			journal, err := os.ReadFile(filepath.Join(m.dataDir, stateFileName))
			if err != nil {
				t.Fatal(err)
			}
			for _, secret := range []string{f.server.URL, "restart-fixture-token", "public-fixture-signature", "private-fixture-signature", "download_url"} {
				if strings.Contains(string(journal), secret) {
					t.Fatalf("transient download URL or credential was journaled: %s", secret)
				}
			}
		})
	}
}

func TestDownloadAfterRestartRejectsChangedSavedTarget(t *testing.T) {
	for _, change := range []string{"tag", "commit", "checksum", "size", "schema", "invalid_manifest"} {
		t.Run(change, func(t *testing.T) {
			f, m := newFixture(t)
			if _, err := m.Check(context.Background()); err != nil {
				t.Fatal(err)
			}
			reloaded, err := New(m.cfg, m.dataDir, m.build)
			if err != nil {
				t.Fatal(err)
			}
			reloaded.apiBase = f.server.URL
			switch change {
			case "tag":
				f.release.TagName = "v1.2.3"
			case "commit":
				f.manifest.Commit = strings.Repeat("b", 40)
			case "checksum":
				f.manifest.Assets[0].SHA256 = strings.Repeat("b", 64)
			case "size":
				f.manifest.Assets[0].Size++
				f.release.Assets[0].Size++
			case "schema":
				f.manifest.DatabaseSchema++
			case "invalid_manifest":
				f.rawManifest = []byte(`{"unknown":true}`)
			}
			snap, err := reloaded.Download(context.Background())
			wantCode := "release_changed"
			if change == "invalid_manifest" {
				wantCode = "invalid_manifest"
			} else if !errors.Is(err, ErrReleaseChanged) {
				t.Fatalf("changed saved release did not require recheck: %v", err)
			}
			if err == nil || snap.State != "failed" || snap.Operation == nil || snap.Operation.ErrorCode != wantCode || reloaded.state.StagedPath != "" {
				t.Fatalf("changed saved release accepted: %#v %v", snap, err)
			}
			if f.requests["/download/core"] != 0 || f.requests["/api/core"] != 0 {
				t.Fatal("artifact requested before saved target was validated")
			}
		})
	}
}

func TestRecoveredReadyAppliesWithoutRefreshingRelease(t *testing.T) {
	f, m := newFixture(t)
	checkReady(t, m)
	stagedPath := m.state.StagedPath
	reloaded, err := New(m.cfg, m.dataDir, m.build)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.apiBase = f.server.URL
	if snap := reloaded.Snapshot(); snap.State != "ready" || reloaded.state.StagedPath != stagedPath || !snap.Release.Assets[0].Verified {
		t.Fatal("recovery discarded a verified staged artifact")
	}
	calls := 0
	reloaded.SetApplier(func(_ context.Context, path string, _ UpdateInfo) error {
		calls++
		if path != stagedPath {
			t.Fatal("recovery changed the staged artifact")
		}
		return nil
	})
	if _, err := reloaded.Apply(context.Background()); err != nil || calls != 1 {
		t.Fatalf("recovered ready artifact was not applied: %v", err)
	}
	if f.requests["/repos/o/r/releases/latest"] != 1 || f.requests["/repos/o/r/releases/tags/v1.2.2"] != 0 || f.requests["/download/core"] != 1 {
		t.Fatal("applying a verified staged artifact fetched the release again")
	}
}

func TestRecoveredDownloadRefreshRemainsSingleFlight(t *testing.T) {
	f, m := newFixture(t)
	if _, err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	reloaded, err := New(m.cfg, m.dataDir, m.build)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.apiBase = f.server.URL
	entered, release := make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	f.before = func(path string) {
		if path == "/repos/o/r/releases/tags/v1.2.2" {
			close(entered)
			<-release
		}
	}
	done := make(chan error, 1)
	go func() { _, err := reloaded.Download(context.Background()); done <- err }()
	select {
	case <-entered:
	case err := <-done:
		t.Fatalf("download did not resolve the recovered release: %v", err)
	}
	for _, operation := range []func(context.Context) (Snapshot, error){reloaded.Check, reloaded.Download, reloaded.Apply, reloaded.Rollback} {
		if _, err := operation(context.Background()); !errors.Is(err, ErrBusy) {
			t.Fatalf("mutation accepted while saved release was resolving: %v", err)
		}
	}
	close(release)
	if err := <-done; err != nil || reloaded.Snapshot().State != "ready" {
		t.Fatalf("saved release download did not complete: %v", err)
	}
}
