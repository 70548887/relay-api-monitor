package update

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"relaylab/internal/config"
)

type releaseFixture struct {
	mu          sync.Mutex
	server      *httptest.Server
	release     githubRelease
	manifest    manifest
	payload     []byte
	failPath    map[string]int
	requests    map[string]int
	headers     map[string]string
	before      func(string)
	rawManifest []byte
}

func newFixture(t *testing.T) (*releaseFixture, *Manager) {
	t.Helper()
	payload := []byte("relaylab verified release executable")
	hash := sha256.Sum256(payload)
	f := &releaseFixture{payload: payload, failPath: map[string]int{}, requests: map[string]int{}, headers: map[string]string{}}
	f.manifest = manifest{Version: "1.2.2", Commit: strings.Repeat("a", 40), BuildTime: "2026-10-26T01:02:03Z", LauncherProtocol: 2, DatabaseSchema: 1, MinDatabaseSchema: 1, Assets: []manifestAsset{{Name: platformAssetName(runtime.GOOS, runtime.GOARCH), OS: runtime.GOOS, Arch: runtime.GOARCH, Size: int64(len(payload)), SHA256: fmt.Sprintf("%x", hash)}}}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests[r.URL.Path]++
		f.headers[r.URL.Path] = r.Header.Get("Authorization")
		code := f.failPath[r.URL.Path]
		before := f.before
		f.mu.Unlock()
		if before != nil {
			before(r.URL.Path)
		}
		if code != 0 {
			w.WriteHeader(code)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		switch r.URL.Path {
		case "/repos/o/r/releases/latest":
			_ = json.NewEncoder(w).Encode(f.release)
		case "/manifest.json", "/api/manifest":
			if f.rawManifest != nil {
				_, _ = w.Write(f.rawManifest)
			} else {
				_ = json.NewEncoder(w).Encode(f.manifest)
			}
		case "/download/core", "/api/core":
			_, _ = w.Write(f.payload)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(f.server.Close)
	f.release = githubRelease{TagName: "v1.2.2", Name: "release", HTMLURL: "https://github.com/o/r/releases/tag/v1.2.2", PublishedAt: "2026-10-26T01:03:00Z", Assets: []githubAsset{
		{Name: f.manifest.Assets[0].Name, Size: int64(len(payload)), BrowserURL: f.server.URL + "/download/core", APIURL: f.server.URL + "/api/core"},
		{Name: "manifest.json", Size: 100, BrowserURL: f.server.URL + "/manifest.json", APIURL: f.server.URL + "/api/manifest"},
	}}
	m, err := New(config.UpdateConfig{Enabled: true, GitHubOwner: "o", GitHubRepo: "r"}, t.TempDir(), BuildInfo{Version: "1.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	m.apiBase = f.server.URL
	return f, m
}
func checkReady(t *testing.T, m *Manager) Snapshot {
	t.Helper()
	if _, err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	snap, err := m.Download(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.State != "ready" || snap.Operation == nil || snap.Operation.Progress != 100 {
		t.Fatalf("not ready: %#v", snap)
	}
	return snap
}
func TestSemVer2Comparison(t *testing.T) {
	if IsNewerVersion("1.2.2", "dev") || IsNewerVersion("dev", "1.2.2") || !IsNewerVersion("1.2.2", "1.2.1") {
		t.Fatal("explicit version comparator must reject dev")
	}
	chain := []string{"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta", "1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0"}
	for i := 1; i < len(chain); i++ {
		if !isNewer(chain[i], chain[i-1]) || isNewer(chain[i-1], chain[i]) {
			t.Fatalf("ordering %s %s", chain[i-1], chain[i])
		}
	}
	for _, bad := range []string{"1.2", "v1.2.3-", "1.2.3+", "01.2.3", "1.2.3-01", "1.2.3-a..b", "1.2.3+abc+def", "1.2.3-a_", "1.2.3.4", "1.2.3-00.foo"} {
		if _, ok := parseVersion(bad); ok {
			t.Fatalf("accepted %q", bad)
		}
	}
	if isNewer("v1.0.0+other", "1.0.0+build") || !isNewer("1.999999999999999999999999999999999999999.0", "1.99.0") || !isNewer("1.2.2", "dev") {
		t.Fatal("build metadata, huge component or dev comparison")
	}
}
func TestExactAssetSelection(t *testing.T) {
	want := platformAssetName(runtime.GOOS, runtime.GOARCH)
	for _, wrong := range []string{"relaylab-launcher-" + runtime.GOOS + "-" + runtime.GOARCH, "extra-" + want, want + ".zip", want + "-extra"} {
		if _, ok := selectAsset([]Asset{{Name: wrong}}, runtime.GOOS, runtime.GOARCH); ok {
			t.Fatalf("selected %s", wrong)
		}
	}
	if got, ok := selectAsset([]Asset{{Name: want}}, runtime.GOOS, runtime.GOARCH); !ok || got.Name != want {
		t.Fatal("exact selection failed")
	}
	if _, ok := selectAsset([]Asset{{Name: "relaylab-linux-amd64.exe"}}, "linux", "amd64"); ok {
		t.Fatal("Windows suffix accepted for Linux")
	}
}
func TestCheckDownloadDurableVerifiedAndPrivatePaths(t *testing.T) {
	_, m := newFixture(t)
	snap := checkReady(t, m)
	if snap.Latest == nil || snap.Latest.SHA256 == "" || snap.Latest.LauncherProtocol != 2 || !snap.Release.Assets[0].Verified {
		t.Fatal("missing verified metadata")
	}
	if snap.StagedPath != "" {
		t.Fatal("public staged path exposed")
	}
	content, err := os.ReadFile(m.state.StagedPath)
	if err != nil || len(content) == 0 {
		t.Fatal("missing staged artifact")
	}
	b, _ := json.Marshal(snap)
	if strings.Contains(string(b), m.dataDir) || strings.Contains(string(b), "local_path") || strings.Contains(string(b), "staged_path") {
		t.Fatal("public local path exposed")
	}
	disk, err := os.ReadFile(filepath.Join(m.dataDir, stateFileName))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(disk), "/download/core") {
		t.Fatal("download URL journaled")
	}
	reloaded, err := New(m.cfg, m.dataDir, m.build)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Snapshot().State != "ready" {
		t.Fatal("verified ready state not recovered")
	}
}
func TestRequiredManifestValidation(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*releaseFixture)
	}{
		{"missing", func(f *releaseFixture) { f.release.Assets = f.release.Assets[:1] }},
		{"404", func(f *releaseFixture) { f.failPath["/manifest.json"] = 404 }},
		{"malformed", func(f *releaseFixture) { f.rawManifest = []byte("{") }},
		{"trailing", func(f *releaseFixture) { b, _ := json.Marshal(f.manifest); f.rawManifest = append(b, []byte("{}")...) }},
		{"duplicate_json_field", func(f *releaseFixture) {
			b, _ := json.Marshal(f.manifest)
			f.rawManifest = []byte(strings.Replace(string(b), "\"version\":\"1.2.2\"", "\"version\":\"1.2.2\",\"version\":\"1.2.2\"", 1))
		}},
		{"wrong_version", func(f *releaseFixture) { f.manifest.Version = "1.2.3" }},
		{"wrong_protocol", func(f *releaseFixture) { f.manifest.LauncherProtocol = 1 }},
		{"schema_min_too_new", func(f *releaseFixture) { f.manifest.DatabaseSchema = 2; f.manifest.MinDatabaseSchema = 2 }},
		{"schema_zero", func(f *releaseFixture) { f.manifest.DatabaseSchema = 0 }},
		{"commit_missing", func(f *releaseFixture) { f.manifest.Commit = "" }},
		{"time_missing", func(f *releaseFixture) { f.manifest.BuildTime = "" }},
		{"hash_missing", func(f *releaseFixture) { f.manifest.Assets[0].SHA256 = "" }},
		{"hash_nonhex", func(f *releaseFixture) { f.manifest.Assets[0].SHA256 = strings.Repeat("z", 64) }},
		{"negative", func(f *releaseFixture) { f.manifest.Assets[0].Size = -1 }},
		{"oversized", func(f *releaseFixture) { f.manifest.Assets[0].Size = maxDownloadSize + 1 }},
		{"size_mismatch", func(f *releaseFixture) { f.manifest.Assets[0].Size++ }},
		{"platform", func(f *releaseFixture) { f.manifest.Assets[0].OS = "unknown" }},
		{"disguised_name", func(f *releaseFixture) { f.manifest.Assets[0].Name = "x-" + f.manifest.Assets[0].Name }},
		{"path_traversal", func(f *releaseFixture) { f.manifest.Assets[0].Name = "../" + f.manifest.Assets[0].Name }},
		{"duplicate", func(f *releaseFixture) { f.manifest.Assets = append(f.manifest.Assets, f.manifest.Assets[0]) }},
		{"duplicate_release", func(f *releaseFixture) { f.release.Assets = append(f.release.Assets, f.release.Assets[0]) }},
		{"draft", func(f *releaseFixture) { f.release.Draft = true }},
		{"prerelease", func(f *releaseFixture) { f.release.Prerelease = true }},
		{"prerelease_tag", func(f *releaseFixture) { f.release.TagName = "v1.2.2-rc.1" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, m := newFixture(t)
			tt.mutate(f)
			snap, err := m.Check(context.Background())
			if err == nil || snap.State != "failed" || snap.Operation == nil || snap.Operation.ErrorCode == "" {
				t.Fatalf("unchecked release accepted: %#v %v", snap, err)
			}
			if m.state.StagedPath != "" {
				t.Fatal("staged without valid manifest")
			}
		})
	}
}
func TestForwardSchemaWithCompatibleMinimum(t *testing.T) {
	f, m := newFixture(t)
	f.manifest.DatabaseSchema = 2
	if snap, err := m.Check(context.Background()); err != nil || snap.Latest.DatabaseSchema != 2 {
		t.Fatalf("forward migration rejected: %v", err)
	}
}
func TestKnownAuxiliaryManifestAssets(t *testing.T) {
	f, m := newFixture(t)
	extra := []manifestAsset{
		{Name: strings.Replace(platformAssetName(runtime.GOOS, runtime.GOARCH), "relaylab-", "relaylab-launcher-", 1), OS: runtime.GOOS, Arch: runtime.GOARCH, Size: 123, SHA256: strings.Repeat("b", 64)},
		{Name: "relaylab-linux-arm64-1.2.2.tar.gz", OS: "linux", Arch: "arm64", Size: 456, SHA256: strings.Repeat("c", 64)},
	}
	for _, a := range extra {
		f.manifest.Assets = append(f.manifest.Assets, a)
		f.release.Assets = append(f.release.Assets, githubAsset{Name: a.Name, Size: a.Size, BrowserURL: f.server.URL + "/auxiliary"})
	}
	snap, err := m.Check(context.Background())
	if err != nil || snap.Latest.AssetName != platformAssetName(runtime.GOOS, runtime.GOARCH) {
		t.Fatalf("auxiliary asset selection %v", err)
	}
}
func TestDownloadRejectsMismatchTruncationAnd404(t *testing.T) {
	for _, kind := range []string{"hash", "truncated", "oversized", "404"} {
		t.Run(kind, func(t *testing.T) {
			f, m := newFixture(t)
			if _, err := m.Check(context.Background()); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "hash":
				f.payload = []byte(strings.Repeat("X", len(f.payload)))
			case "truncated":
				f.payload = f.payload[:len(f.payload)-1]
			case "oversized":
				f.payload = append(f.payload, '!')
			case "404":
				f.failPath["/download/core"] = 404
			}
			snap, err := m.Download(context.Background())
			if err == nil || snap.State != "failed" || snap.Operation == nil || snap.Operation.ErrorCode == "" || m.state.StagedPath != "" {
				t.Fatalf("bad download ready: %#v %v", snap, err)
			}
			entries, _ := filepath.Glob(filepath.Join(m.dataDir, "updates", "*", ".download-*"))
			if len(entries) > 0 {
				t.Fatal("partial file leaked")
			}
		})
	}
}
func TestCheckClearsDifferentTargetAndPreservesSameTarget(t *testing.T) {
	f, m := newFixture(t)
	checkReady(t, m)
	staged := m.state.StagedPath
	snap, err := m.Check(context.Background())
	if err != nil || snap.State != "ready" || m.state.StagedPath != staged {
		t.Fatal("same target lost staging")
	}
	f.manifest.Commit = strings.Repeat("b", 40)
	snap, err = m.Check(context.Background())
	if err != nil || snap.State != "available" || m.state.StagedPath != "" || snap.Release.Assets[0].Verified {
		t.Fatal("changed target retained staging")
	}
	if _, err = m.Apply(context.Background()); !errors.Is(err, ErrNoRelease) {
		t.Fatal("stale staging applied")
	}
}
func TestApplyRequiresEnabledVerifiedReadyAndRechecksHash(t *testing.T) {
	_, m := newFixture(t)
	checkReady(t, m)
	calls := 0
	m.SetApplier(func(context.Context, string, UpdateInfo) error { calls++; return nil })
	m.cfg.Enabled = false
	if _, err := m.Apply(context.Background()); !errors.Is(err, ErrDisabled) {
		t.Fatal(err)
	}
	m.cfg.Enabled = true
	if err := os.WriteFile(m.state.StagedPath, []byte(strings.Repeat("X", int(m.state.Latest.AssetSize))), 0600); err != nil {
		t.Fatal(err)
	}
	snap, err := m.Apply(context.Background())
	if err == nil || snap.State != "failed" || calls != 0 || snap.Operation == nil || snap.Operation.ErrorCode != "checksum_mismatch" {
		t.Fatalf("unverified apply: %#v %v", snap, err)
	}
}
func TestApplyWithoutLauncherRemainsReady(t *testing.T) {
	_, m := newFixture(t)
	checkReady(t, m)
	snap, err := m.Apply(context.Background())
	if !errors.Is(err, ErrApplyUnavailable) || snap.State != "ready" {
		t.Fatalf("fake pending restart %#v %v", snap, err)
	}
}
func TestApplyAcknowledgementAndLauncherAuthority(t *testing.T) {
	_, m := newFixture(t)
	checkReady(t, m)
	var received UpdateInfo
	m.SetApplier(func(_ context.Context, _ string, info UpdateInfo) error { received = info; return nil })
	snap, err := m.Apply(context.Background())
	if err != nil || snap.State != "validating" || received.OperationID == "" || snap.Operation.ID != received.OperationID {
		t.Fatalf("acceptance failed: %#v %v", snap, err)
	}
	if received.Commit == "" || received.LauncherProtocol != 2 || received.DatabaseSchema != 1 {
		t.Fatal("bridge metadata missing")
	}
	l := LauncherStatus{OperationID: received.OperationID, State: "failed", ErrorCode: "health_failed", Message: "worker did not come online", UpdatedAt: time.Now().UTC()}
	b, _ := json.Marshal(l)
	if err = os.WriteFile(filepath.Join(m.dataDir, launcherStateFileName), b, 0600); err != nil {
		t.Fatal(err)
	}
	snap = m.Snapshot()
	if snap.State != "failed" || snap.Operation.ErrorCode != "health_failed" {
		t.Fatal("launcher result missing")
	}
	if _, err = m.Check(context.Background()); err != nil {
		t.Fatal("terminal launcher state blocked next operation", err)
	}
	if m.Snapshot().State == "failed" {
		t.Fatal("old launcher result shadowed check")
	}
}
func TestRestartNeverInfersSuccessfulUpdateFromVersion(t *testing.T) {
	_, m := newFixture(t)
	checkReady(t, m)
	m.SetApplier(func(context.Context, string, UpdateInfo) error { return nil })
	if _, err := m.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	nextBuild := m.build
	nextBuild.Version = m.state.Latest.LatestVersion
	reloaded, err := New(m.cfg, m.dataDir, nextBuild)
	if err != nil {
		t.Fatal(err)
	}
	snap := reloaded.Snapshot()
	if snap.State != "failed" || snap.Operation == nil || snap.Operation.ErrorCode != "operation_interrupted" {
		t.Fatalf("version-only success: %#v", snap)
	}
}
func TestRestartMirrorsLauncherTerminalWithoutOverwritingIt(t *testing.T) {
	_, m := newFixture(t)
	checkReady(t, m)
	m.SetApplier(func(context.Context, string, UpdateInfo) error { return nil })
	if _, err := m.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	l := LauncherStatus{OperationID: m.state.LauncherOperationID, State: "succeeded", Version: "1.2.2", Progress: 100, UpdatedAt: time.Now().UTC()}
	journal, _ := json.Marshal(l)
	path := filepath.Join(m.dataDir, launcherStateFileName)
	if err := os.WriteFile(path, journal, 0600); err != nil {
		t.Fatal(err)
	}
	reloaded, err := New(m.cfg, m.dataDir, BuildInfo{Version: "1.2.2"})
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Snapshot().State != "succeeded" {
		t.Fatal("launcher confirmed success not mirrored")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(journal) {
		t.Fatal("manager overwrote launcher journal")
	}
}
func TestRollbackAcceptanceIsNotCompletion(t *testing.T) {
	_, m := newFixture(t)
	var id string
	m.SetRollbackWithID(func(_ context.Context, op string) error { id = op; return nil })
	snap, err := m.Rollback(context.Background())
	if err != nil || snap.State != "validating" || id == "" || snap.Operation.ID != id {
		t.Fatalf("premature rollback %#v %v", snap, err)
	}
	if err = m.SyncLauncher(LauncherStatus{OperationID: id, State: "rolled_back", Progress: 100}); err != nil {
		t.Fatal(err)
	}
	if m.Snapshot().State != "rolled_back" {
		t.Fatal("confirmed rollback missing")
	}
}
func TestAllMutationsSingleFlight(t *testing.T) {
	f, m := newFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	f.before = func(path string) {
		if path == "/repos/o/r/releases/latest" {
			close(entered)
			<-release
		}
	}
	m.SetRollback(func(context.Context) error { return nil })
	done := make(chan error, 1)
	go func() { _, err := m.Check(context.Background()); done <- err }()
	<-entered
	for _, operation := range []func(context.Context) (Snapshot, error){m.Check, m.Download, m.Apply, m.Rollback} {
		if _, err := operation(context.Background()); !errors.Is(err, ErrBusy) {
			t.Fatalf("concurrent mutation accepted: %v", err)
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestStateDurabilityFailsBeforeNetworkOrApply(t *testing.T) {
	f, m := newFixture(t)
	if err := os.Mkdir(filepath.Join(m.dataDir, stateFileName), 0700); err != nil {
		t.Fatal(err)
	}
	snap, err := m.Check(context.Background())
	if !errors.Is(err, ErrStateDurability) || snap.State != "failed" || snap.Operation == nil || snap.Operation.ErrorCode != "state_write_failed" || len(f.requests) != 0 {
		t.Fatalf("failed state write proceeded: %#v %v", snap, err)
	}
}
func TestDownloadStateWriteFailureNeverReady(t *testing.T) {
	f, m := newFixture(t)
	if _, err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	f.before = func(path string) {
		if path == "/download/core" {
			_ = os.Remove(filepath.Join(m.dataDir, stateFileName))
			_ = os.Mkdir(filepath.Join(m.dataDir, stateFileName), 0700)
		}
	}
	snap, err := m.Download(context.Background())
	if !errors.Is(err, ErrStateDurability) || snap.State == "ready" || m.state.StagedPath != "" {
		t.Fatalf("non-durable staging accepted: %#v %v", snap, err)
	}
}
func TestAllowedGitHubHTTPSAndRedirectAuthorization(t *testing.T) {
	_, m := newFixture(t)
	m.apiBase = ""
	m.cfg.GitHubToken = "fixture-secret"
	for _, raw := range []string{"http://api.github.com/a", "https://foo.github.com/a", "https://raw.githubusercontent.com/a", "https://evil.githubusercontent.com/a", "https://api.github.com.evil/a", "https://u:p@github.com/a", "https://github.com:444/a"} {
		if _, err := m.request(context.Background(), raw, "application/json"); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	for _, host := range []string{"api.github.com", "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com"} {
		req, _ := http.NewRequest(http.MethodGet, "https://"+host+"/asset?sig=fake", nil)
		req.Header.Set("Authorization", "Bearer fixture-secret")
		if err := m.checkRedirect(req, nil); err != nil {
			t.Fatal(err)
		}
		if host != "api.github.com" && req.Header.Get("Authorization") != "" {
			t.Fatal("token forwarded to asset host")
		}
	}
	req, _ := http.NewRequest(http.MethodGet, "http://github.com/a", nil)
	if m.checkRedirect(req, nil) == nil {
		t.Fatal("HTTPS downgrade accepted")
	}
}
func TestPrivateAssetsUseAPIURLs(t *testing.T) {
	f, m := newFixture(t)
	m.cfg.GitHubToken = "private-fixture-token"
	f.failPath["/manifest.json"] = 404
	f.failPath["/download/core"] = 404
	checkReady(t, m)
	for _, path := range []string{"/repos/o/r/releases/latest", "/api/manifest", "/api/core"} {
		if f.headers[path] != "Bearer private-fixture-token" {
			t.Fatal("private API asset auth missing", path)
		}
	}
}
func TestErrorsRedactTokenProxyCredentialsAndSignedQuery(t *testing.T) {
	_, m := newFixture(t)
	m.cfg.GitHubToken = "secret-fixture-token"
	m.cfg.Proxy = "http://user:proxy-fixture-password@proxy.example"
	err := m.safeError(errors.New("token secret-fixture-token Bearer other-fixture-token https://u:p@release-assets.githubusercontent.com/path?sig=secret-signature&token=value proxy-fixture-password"))
	for _, secret := range []string{"secret-fixture-token", "other-fixture-token", "secret-signature", "proxy-fixture-password", "u:p@"} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("secret exposed: %s", err)
		}
	}
}

func TestEveryLauncherPhaseBlocksMutations(t *testing.T) {
	for _, phase := range []string{"accepted", "pending_restart", "draining", "stopped", "backed_up", "replacing", "starting", "validating", "rolling_back", "recovery_required"} {
		t.Run(phase, func(t *testing.T) {
			_, m := newFixture(t)
			m.state.LauncherOperationID = "launcher-op"
			if err := m.SyncLauncher(LauncherStatus{OperationID: "launcher-op", State: phase}); err != nil {
				t.Fatal(err)
			}
			for _, operation := range []func(context.Context) (Snapshot, error){m.Check, m.Download, m.Apply, m.Rollback} {
				if _, err := operation(context.Background()); !errors.Is(err, ErrBusy) {
					t.Fatalf("%s mutation was not blocked: %v", phase, err)
				}
			}
		})
	}
}

func TestFinishedOperationRetainsErrorAndIdentityAfterRestart(t *testing.T) {
	f, m := newFixture(t)
	f.failPath["/manifest.json"] = 404
	snap, err := m.Check(context.Background())
	if err == nil || snap.Operation == nil {
		t.Fatal("failure missing")
	}
	id, code, message := snap.Operation.ID, snap.Operation.ErrorCode, snap.Operation.Message
	reloaded, err := New(m.cfg, m.dataDir, m.build)
	if err != nil {
		t.Fatal(err)
	}
	after := reloaded.Snapshot()
	if after.Operation == nil || after.Operation.ID != id || after.Operation.ErrorCode != code || after.Operation.Message != message {
		t.Fatal("finished failure diagnostics lost")
	}
}

func TestRecoveredJournalMessagesAreSanitized(t *testing.T) {
	_, m := newFixture(t)
	m.cfg.GitHubToken = "fake-recovery-token"
	m.state.Operation = &Operation{ID: "recovered-op", State: "failed", Message: "fake-recovery-token https://u:p@github.com/path?sig=signed-value"}
	m.state.State = "failed"
	if err := m.persistLocked(); err != nil {
		t.Fatal(err)
	}
	reloaded, err := New(m.cfg, m.dataDir, m.build)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(reloaded.Snapshot())
	for _, secret := range []string{"fake-recovery-token", "signed-value", "u:p@"} {
		if strings.Contains(string(b), secret) {
			t.Fatal("legacy journal credential exposed")
		}
	}
}

func TestLauncherTerminalHistorySurvivesNextCheck(t *testing.T) {
	_, m := newFixture(t)
	checkReady(t, m)
	m.SetApplier(func(context.Context, string, UpdateInfo) error { return nil })
	if _, err := m.Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	id := m.state.LauncherOperationID
	if err := m.SyncLauncher(LauncherStatus{OperationID: id, State: "succeeded", Progress: 100, UpdatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, h := range m.Snapshot().History {
		if h.ID == id && h.State == "succeeded" {
			found = true
		}
	}
	if !found {
		t.Fatal("authoritative terminal history lost")
	}
}
