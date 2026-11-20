package fingerprint

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// testBuild is a reference bank small enough to write by hand. Its detector loads but cannot score.
type testBuild struct {
	builtAt string
	models  []Model
}

var (
	buildA = testBuild{builtAt: "2026-01-01T00:00:00Z", models: []Model{
		{ID: "gpt-5", DisplayName: "GPT-5", Family: "openai", FamilyName: "OpenAI"},
		{ID: "claude-sonnet-4", DisplayName: "Claude Sonnet 4", Family: "anthropic", FamilyName: "Anthropic"},
	}}
	buildB = testBuild{builtAt: "2026-02-01T00:00:00Z", models: append(append([]Model(nil), buildA.models...),
		Model{ID: "gemini-3-pro", DisplayName: "Gemini 3 Pro", Family: "google", FamilyName: "Google"})}
)

const testResponseCount = 30

func (b testBuild) detector(t *testing.T) []byte {
	t.Helper()
	ids := make([]string, len(b.models))
	for i, model := range b.models {
		ids[i] = model.ID
	}
	header, err := json.Marshal(map[string]any{
		"schema": "shared-detector-v2", "bank_built_at": b.builtAt, "model_ids": ids,
		"response_counts": map[string]any{"$f32": 0, "n": len(ids)},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := binary.LittleEndian.AppendUint32(nil, uint32(len(header)))
	out = append(out, header...)
	for len(out)%4 != 0 {
		out = append(out, 0)
	}
	// Float32 values are stored byte plane by byte plane.
	count, bits := len(ids), math.Float32bits(testResponseCount)
	planes := make([]byte, 4*count)
	for i := 0; i < count; i++ {
		for plane := 0; plane < 4; plane++ {
			planes[plane*count+i] = byte(bits >> (8 * plane))
		}
	}
	return append(out, planes...)
}

func (b testBuild) index(t *testing.T, withFiles bool) []byte {
	t.Helper()
	models := make([]map[string]any, len(b.models))
	for i, model := range b.models {
		models[i] = map[string]any{"id": model.ID, "display_name": model.DisplayName, "family": model.Family,
			"family_name": model.FamilyName, "response_count": testResponseCount}
	}
	document := map[string]any{"built_at": b.builtAt, "models": models}
	if withFiles {
		sum := sha256.Sum256(b.detector(t))
		document["schema"], document["detector_schema"] = "lmfpd-data-v1", "shared-detector-v2"
		document["files"] = map[string]any{detectorFile: map[string]any{"parts": []string{"part-0"}, "sha256": hex.EncodeToString(sum[:])}}
	}
	out, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func (b testBuild) writeDir(t *testing.T, dir string) {
	t.Helper()
	for name, content := range map[string][]byte{localBankFile: b.index(t, false), detectorFile: b.detector(t)} {
		if err := os.WriteFile(filepath.Join(dir, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// dataServer serves one build the way the lm-detector data site does.
type dataServer struct {
	*httptest.Server
	mu       sync.Mutex
	build    testBuild
	failing  bool
	requests int
	hold     chan struct{}
}

func newDataServer(t *testing.T, build testBuild) *dataServer {
	t.Helper()
	s := &dataServer{build: build}
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests++
		build, failing, hold := s.build, s.failing, s.hold
		s.mu.Unlock()
		if hold != nil {
			<-hold
		}
		switch {
		case failing:
			http.Error(w, "unavailable", http.StatusServiceUnavailable)
		case r.URL.Path == "/bundle.json":
			_, _ = w.Write(build.index(t, true))
		case r.URL.Path == "/chunks/part-0":
			_, _ = w.Write(encoder.EncodeAll(build.detector(t), nil))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(func() {
		s.Close()
		_ = encoder.Close()
	})
	return s
}

func (s *dataServer) set(change func()) {
	s.mu.Lock()
	defer s.mu.Unlock()
	change()
}

func (s *dataServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.requests
}

func onlineBank(dir string, s *dataServer) *Bank {
	b := NewBank(dir)
	b.sources = []string{s.URL + "/"}
	return b
}

// offlineBank fails every download, so a test that uses it proves the cache file alone was enough.
func offlineBank(dir string) *Bank {
	b := NewBank(dir)
	b.sources = []string{"http://127.0.0.1:1/"}
	return b
}

func wantBuild(t *testing.T, ref *Reference, build testBuild) {
	t.Helper()
	if ref.BuiltAt != build.builtAt {
		t.Fatalf("built at %q, want %q", ref.BuiltAt, build.builtAt)
	}
	models := ref.Models()
	if len(models) != len(build.models) {
		t.Fatalf("%d models, want %d", len(models), len(build.models))
	}
	for i, model := range build.models {
		if models[i] != model {
			t.Fatalf("model %d is %+v, want %+v", i, models[i], model)
		}
		if found, ok := ref.Model(model.ID); !ok || found != model {
			t.Fatalf("lookup of %q returned %+v, %v", model.ID, found, ok)
		}
	}
	if _, ok := ref.Model("not-in-the-bank"); ok {
		t.Fatal("lookup of an unknown model succeeded")
	}
	if ref.UpdatedAt.IsZero() {
		t.Fatal("updated at is zero")
	}
}

func TestReferenceReadsLocalDirectoryAndFollowsItsFiles(t *testing.T) {
	dir, ctx := t.TempDir(), context.Background()
	buildA.writeDir(t, dir)
	bank := offlineBank(t.TempDir())
	ref, err := bank.Reference(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	wantBuild(t, ref, buildA)
	again, err := bank.Reference(ctx, dir)
	if err != nil || again != ref {
		t.Fatalf("unchanged files were read again: %v", err)
	}
	buildB.writeDir(t, dir)
	if ref, err = bank.Reference(ctx, dir); err != nil {
		t.Fatal(err)
	}
	wantBuild(t, ref, buildB)
	status := bank.Status(ctx, dir)
	if !status.Ready || status.Source != "local" || status.BuiltAt != buildB.builtAt || status.Models != 3 || status.UpdatedAt == nil || status.Error != "" {
		t.Fatalf("status %+v", status)
	}
}

func TestReferenceRejectsBrokenLocalDirectory(t *testing.T) {
	ctx := context.Background()
	bank := offlineBank(t.TempDir())
	if _, err := bank.Reference(ctx, "relative/dir"); !errors.Is(err, ErrBankUnavailable) {
		t.Fatalf("relative directory: %v", err)
	}
	// A URL would make the SDK download from it.
	if _, err := bank.Reference(ctx, "https://example.com/data/"); !errors.Is(err, ErrBankUnavailable) {
		t.Fatalf("URL as local directory: %v", err)
	}
	empty := t.TempDir()
	if _, err := bank.Reference(ctx, empty); !errors.Is(err, ErrBankUnavailable) {
		t.Fatalf("empty directory: %v", err)
	}
	if status := bank.Status(ctx, empty); status.Ready || status.Source != "local" || status.Error == "" {
		t.Fatalf("status %+v", status)
	}
	mismatched := t.TempDir()
	buildA.writeDir(t, mismatched)
	if err := os.WriteFile(filepath.Join(mismatched, detectorFile), buildB.detector(t), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := bank.Reference(ctx, mismatched); !errors.Is(err, ErrBankUnavailable) {
		t.Fatalf("detector of another build: %v", err)
	}
}

func TestReferenceDownloadsOnceAndThenUsesTheCache(t *testing.T) {
	dir, ctx := t.TempDir(), context.Background()
	server := newDataServer(t, buildA)
	bank := onlineBank(dir, server)
	if status := bank.Status(ctx, ""); status.Ready || status.Source != "online" || status.Error != "" || server.count() != 0 {
		t.Fatalf("status before any download %+v after %d requests", status, server.count())
	}
	ref, err := bank.Reference(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	wantBuild(t, ref, buildA)
	downloaded := server.count()
	if downloaded != 2 {
		t.Fatalf("%d requests for the first download, want 2", downloaded)
	}
	if again, err := bank.Reference(ctx, ""); err != nil || again != ref {
		t.Fatalf("cached build was loaded again: %v", err)
	}
	restarted := offlineBank(dir)
	if ref, err = restarted.Reference(ctx, ""); err != nil {
		t.Fatal(err)
	}
	wantBuild(t, ref, buildA)
	if status := restarted.Status(ctx, ""); !status.Ready || status.BuiltAt != buildA.builtAt || status.Models != 2 {
		t.Fatalf("status %+v", status)
	}
	if server.count() != downloaded {
		t.Fatalf("%d requests after the first download, want none", server.count()-downloaded)
	}
}

func TestRefreshReplacesTheBuildForEveryProcess(t *testing.T) {
	dir, ctx := t.TempDir(), context.Background()
	server := newDataServer(t, buildA)
	bank, other := onlineBank(dir, server), offlineBank(dir)
	if _, err := bank.Reference(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if ref, err := other.Reference(ctx, ""); err != nil {
		t.Fatal(err)
	} else {
		wantBuild(t, ref, buildA)
	}
	if _, changed, err := bank.Refresh(ctx); err != nil || changed {
		t.Fatalf("refresh of an unchanged build: changed %v, %v", changed, err)
	}
	server.set(func() { server.build = buildB })
	ref, changed, err := bank.Refresh(ctx)
	if err != nil || !changed {
		t.Fatalf("refresh of a new build: changed %v, %v", changed, err)
	}
	wantBuild(t, ref, buildB)
	for name, b := range map[string]*Bank{"refreshing": bank, "other": other} {
		current, err := b.Reference(ctx, "")
		if err != nil {
			t.Fatalf("%s process: %v", name, err)
		}
		wantBuild(t, current, buildB)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || entries[0].Name() != cacheFile {
		t.Fatalf("bank directory holds %v, %v; want only %s", entries, err, cacheFile)
	}
}

func TestFailedRefreshKeepsTheCachedBuild(t *testing.T) {
	dir, ctx := t.TempDir(), context.Background()
	server := newDataServer(t, buildA)
	bank := onlineBank(dir, server)
	if _, err := bank.Reference(ctx, ""); err != nil {
		t.Fatal(err)
	}
	server.set(func() { server.failing = true })
	if _, _, err := bank.Refresh(ctx); !errors.Is(err, ErrBankUnavailable) {
		t.Fatalf("refresh from a failing source: %v", err)
	}
	ref, err := bank.Reference(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	wantBuild(t, ref, buildA)
}

func TestFailedDownloadIsNotRepeatedAtOnce(t *testing.T) {
	dir, ctx := t.TempDir(), context.Background()
	server := newDataServer(t, buildA)
	server.set(func() { server.failing = true })
	bank := onlineBank(dir, server)
	if _, err := bank.Reference(ctx, ""); !errors.Is(err, ErrBankUnavailable) {
		t.Fatalf("download from a failing source: %v", err)
	}
	failed := server.count()
	if _, err := bank.Reference(ctx, ""); !errors.Is(err, ErrBankUnavailable) || server.count() != failed {
		t.Fatalf("second use: %v after %d more requests", err, server.count()-failed)
	}
	if status := bank.Status(ctx, ""); status.Ready || status.Error != "" || server.count() != failed {
		t.Fatalf("status %+v after %d more requests", status, server.count()-failed)
	}
	server.set(func() { server.failing = false })
	bank.mu.Lock()
	bank.call.finished = time.Now().Add(-2 * retryDelay)
	bank.mu.Unlock()
	ref, err := bank.Reference(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	wantBuild(t, ref, buildA)
}

func TestReferenceStopsWaitingWhenTheContextEnds(t *testing.T) {
	dir := t.TempDir()
	server := newDataServer(t, buildA)
	hold := make(chan struct{})
	server.set(func() { server.hold = hold })
	bank := onlineBank(dir, server)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := bank.Reference(ctx, ""); !errors.Is(err, ErrBankUnavailable) {
		t.Fatalf("reference with an expired context: %v", err)
	}
	server.set(func() { server.hold = nil })
	close(hold)
	ref, err := bank.Reference(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	wantBuild(t, ref, buildA)
	if server.count() != 2 {
		t.Fatalf("%d requests, want the 2 of one download", server.count())
	}
}

func TestReadCacheHeaderRejectsDamagedFiles(t *testing.T) {
	dir := t.TempDir()
	index := buildA.index(t, true)
	cases := map[string][]byte{
		"empty":           {},
		"zero length":     {0, 0, 0, 0},
		"huge length":     {0xff, 0xff, 0xff, 0xff},
		"truncated index": append(binary.LittleEndian.AppendUint32(nil, uint32(len(index))), index[:len(index)/2]...),
		"not JSON":        append(binary.LittleEndian.AppendUint32(nil, 4), "nope"...),
		"no models":       append(binary.LittleEndian.AppendUint32(nil, 13), `{"models":[]}`...),
	}
	for name, content := range cases {
		path := filepath.Join(dir, "cache.bin")
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := readCacheHeader(path); err == nil {
			t.Fatalf("%s: header was accepted", name)
		}
	}
}

// TestRealReferenceBankDownload downloads the published reference bank. It needs the network, so it only runs with
// RELAYLAB_REAL_FINGERPRINT_DOWNLOAD_TEST=1.
func TestRealReferenceBankDownload(t *testing.T) {
	if os.Getenv("RELAYLAB_REAL_FINGERPRINT_DOWNLOAD_TEST") != "1" {
		t.Skip("set RELAYLAB_REAL_FINGERPRINT_DOWNLOAD_TEST=1 to download the published reference bank")
	}
	dir := os.Getenv("RELAYLAB_FINGERPRINT_TEST_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	ref, err := NewBank(dir).Reference(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	models := ref.Models()
	if ref.BuiltAt == "" || len(models) < 10 {
		t.Fatalf("built at %q with %d models", ref.BuiltAt, len(models))
	}
	families := map[string]int{}
	for _, model := range models {
		if model.ID == "" || model.DisplayName == "" || model.Family == "" || model.FamilyName == "" {
			t.Fatalf("incomplete model %+v", model)
		}
		families[model.FamilyName]++
	}
	t.Logf("built at %s, %d models, families %v", ref.BuiltAt, len(models), families)
	for _, model := range models {
		t.Logf("%s | %s | %s", model.ID, model.DisplayName, model.FamilyName)
	}
}
