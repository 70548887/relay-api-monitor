package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"relaylab/internal/config"
)

// Opt-in integration test: bytes and metadata come from the real GitHub
// Release. The test transport only throttles reading an authenticated HTTPS
// response; it never fabricates metadata, replaces bytes or relaxes host checks.
func TestRealGitHubTransferBeyondMetadataTimeout(t *testing.T) {
	if os.Getenv("RELAYLAB_REAL_GITHUB_DOWNLOAD_TEST") != "1" {
		t.Skip("set RELAYLAB_REAL_GITHUB_DOWNLOAD_TEST=1 with a release tag and evidence directory")
	}
	tag, output := os.Getenv("RELAYLAB_GITHUB_DOWNLOAD_TEST_TAG"), os.Getenv("RELAYLAB_GITHUB_DOWNLOAD_TEST_OUTPUT")
	if tag == "" || output == "" {
		t.Fatal("release tag and evidence directory are required")
	}
	if err := os.MkdirAll(output, 0700); err != nil {
		t.Fatal(err)
	}
	m, err := New(config.UpdateConfig{Enabled: true, GitHubOwner: "70548887", GitHubRepo: "relay-api-monitor", Proxy: os.Getenv("RELAYLAB_GITHUB_TEST_PROXY")}, t.TempDir(), BuildInfo{Version: "dev", OS: runtime.GOOS, Arch: runtime.GOARCH})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	release, err := m.fetchRelease(ctx, tag)
	if err != nil {
		t.Fatal(err)
	}
	asset, ok := selectAsset(release.Assets, runtime.GOOS, runtime.GOARCH)
	if !ok {
		t.Fatal("no matching executable")
	}
	var throttledBytes atomic.Int64
	m.client.Transport = &slowGitHubAssetTransport{base: m.client.Transport, rate: 128 * 1024, bytes: &throttledBytes}
	started := time.Now()
	file, err := m.download(ctx, asset, "real-slow-github-transfer")
	elapsed := time.Since(started)
	report := map[string]any{"tag": tag, "commit": release.Commit, "asset": asset.Name, "sha256": asset.SHA256, "expected_bytes": asset.Size, "elapsed_seconds": elapsed.Seconds(), "throttled_bytes": throttledBytes.Load(), "rate_bytes_per_second": 128 * 1024, "source": "real GitHub HTTPS release response; only client reading speed is limited", "error": ""}
	if err != nil {
		report["error"] = err.Error()
	}
	if file != "" {
		if info, e := os.Stat(file); e == nil {
			report["actual_bytes"] = info.Size()
		}
	}
	b, _ := json.MarshalIndent(report, "", "  ")
	if e := os.WriteFile(filepath.Join(output, "real-slow-download.json"), b, 0600); e != nil {
		t.Fatal(e)
	}
	if err != nil {
		t.Fatal(err)
	}
	if elapsed <= metadataRequestTimeout || throttledBytes.Load() != asset.Size {
		t.Fatalf("slow transfer not demonstrated: elapsed=%v throttled=%d expected=%d", elapsed, throttledBytes.Load(), asset.Size)
	}
	fmt.Printf("PASS REAL GITHUB slow transfer: tag=%s bytes=%d elapsed=%s SHA-256 verified\n", tag, asset.Size, elapsed.Round(time.Second))
}

type slowGitHubAssetTransport struct {
	base  http.RoundTripper
	rate  int64
	bytes *atomic.Int64
}

func (t *slowGitHubAssetTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(req)
	if err == nil && response.StatusCode == 200 && response.ContentLength > 5<<20 {
		response.Body = &rateLimitedGitHubBody{ReadCloser: response.Body, ctx: req.Context(), started: time.Now(), rate: t.rate, bytes: t.bytes}
	}
	return response, err
}

type rateLimitedGitHubBody struct {
	io.ReadCloser
	ctx        context.Context
	started    time.Time
	read, rate int64
	bytes      *atomic.Int64
}

func (r *rateLimitedGitHubBody) Read(b []byte) (int, error) {
	n, err := r.ReadCloser.Read(b)
	r.read += int64(n)
	r.bytes.Add(int64(n))
	if delay := time.Duration(float64(r.read)/float64(r.rate)*float64(time.Second)) - time.Since(r.started); delay > 0 {
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-r.ctx.Done():
			timer.Stop()
			return n, r.ctx.Err()
		}
	}
	return n, err
}
