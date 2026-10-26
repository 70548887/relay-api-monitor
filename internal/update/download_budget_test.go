package update

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestArtifactTransferOutlivesMetadataBudget(t *testing.T) {
	f, m := newFixture(t)
	if _, err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.client.Timeout = 100 * time.Millisecond
	f.before = func(path string) {
		if path == "/download/core" {
			time.Sleep(300 * time.Millisecond)
		}
	}
	started := time.Now()
	snapshot, err := m.Download(context.Background())
	if err != nil || snapshot.State != "ready" {
		t.Fatalf("transfer inherited short metadata timeout: state=%s err=%v", snapshot.State, err)
	}
	if time.Since(started) < 300*time.Millisecond {
		t.Fatal("slow fixture was not exercised")
	}
	if m.client.Timeout != 100*time.Millisecond {
		t.Fatal("shared metadata timeout was mutated")
	}
	if !snapshot.Release.Assets[0].Verified {
		t.Fatal("transfer did not verify manifest digest")
	}
}

func TestMetadataStillHonorsItsOwnBudget(t *testing.T) {
	f, m := newFixture(t)
	m.client.Timeout = 30 * time.Millisecond
	f.before = func(string) { time.Sleep(100 * time.Millisecond) }
	if _, err := m.Check(context.Background()); err == nil {
		t.Fatal("metadata timeout was removed")
	}
	if m.Snapshot().State != "failed" {
		t.Fatal("metadata timeout left a busy state")
	}
}

func TestTransferCancellationCleansTemporaryFile(t *testing.T) {
	f, m := newFixture(t)
	if _, err := m.Check(context.Background()); err != nil {
		t.Fatal(err)
	}
	m.client.Timeout = 10 * time.Millisecond
	f.before = func(path string) {
		if path == "/download/core" {
			time.Sleep(150 * time.Millisecond)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	snapshot, err := m.Download(ctx)
	if err == nil || snapshot.State != "failed" || snapshot.Operation.ErrorCode != "timeout" {
		t.Fatalf("timeout not finalized: state=%s err=%v", snapshot.State, err)
	}
	var partials []string
	_ = filepath.WalkDir(m.dataDir, func(path string, entry os.DirEntry, err error) error {
		if err == nil && !entry.IsDir() && entry.Name() != "update-state.json" {
			partials = append(partials, path)
		}
		return err
	})
	if len(partials) != 0 {
		t.Fatalf("partial downloads remained: %v", partials)
	}
	if snapshot.StagedPath != "" {
		t.Fatal("canceled artifact can be installed")
	}
}

func TestDownloadClientPreservesRedirectRestrictions(t *testing.T) {
	_, m := newFixture(t)
	client := *m.client
	client.Timeout = 0
	client.Transport = updateRoundTripper(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 302, Header: http.Header{"Location": []string{"https://untrusted.example/artifact"}}, Body: io.NopCloser(&emptyReader{})}, nil
	})
	if _, err := m.requestUsing(context.Background(), "https://api.github.com/repos/o/r/releases/assets/1", "application/octet-stream", &client); err == nil {
		t.Fatal("extended transfer allowed an untrusted redirect")
	}
}

type emptyReader struct{}

func (*emptyReader) Read([]byte) (int, error) { return 0, io.EOF }
