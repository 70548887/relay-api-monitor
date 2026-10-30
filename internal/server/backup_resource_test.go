package server

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"relaylab/internal/backup"
	"relaylab/internal/backups"
	"relaylab/internal/domain"
	"relaylab/internal/security"
	"relaylab/internal/store"
)

// TestBackupResourceAcceptance is deliberately opt-in: it writes 96 MiB of
// incompressible fixture data and transfers it twice over a disk-backed slow S3
// double. No production directory, provider or model API is used.
func TestBackupResourceAcceptance(t *testing.T) {
	if os.Getenv("RELAYLAB_BACKUP_RESOURCE_TEST") != "1" {
		t.Skip("set RELAYLAB_BACKUP_RESOURCE_TEST=1 for large database/slow storage acceptance")
	}
	const payloadSize int64 = 96 << 20
	const networkRate int64 = 2 << 20
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	directory := t.TempDir()
	dataDir, objectDir := filepath.Join(directory, "data"), filepath.Join(directory, "objects")
	if e := os.MkdirAll(objectDir, 0700); e != nil {
		t.Fatal(e)
	}
	var uploaded, downloaded, putNanos, getNanos atomic.Int64
	objectPath := func(key string) string {
		h := sha256.Sum256([]byte(key))
		return filepath.Join(objectDir, hex.EncodeToString(h[:]))
	}
	copySlow := func(ctx context.Context, destination io.Writer, source io.Reader, bytes *atomic.Int64) error {
		buffer := make([]byte, 64<<10)
		started := time.Now()
		var total int64
		for {
			n, readErr := source.Read(buffer)
			if n > 0 {
				written, e := destination.Write(buffer[:n])
				bytes.Add(int64(written))
				total += int64(written)
				if e != nil {
					return e
				}
				if written != n {
					return io.ErrShortWrite
				}
				due := time.Duration(float64(total) / float64(networkRate) * float64(time.Second))
				if delay := due - time.Since(started); delay > 0 {
					timer := time.NewTimer(delay)
					select {
					case <-timer.C:
					case <-ctx.Done():
						timer.Stop()
						return ctx.Err()
					}
				}
			}
			if readErr == io.EOF {
				return nil
			}
			if readErr != nil {
				return readErr
			}
		}
	}
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			http.Error(w, "fixture signature required", 403)
			return
		}
		name := objectPath(r.URL.Path)
		switch r.Method {
		case http.MethodPut:
			if r.Header.Get("If-None-Match") != "*" {
				http.Error(w, "conditional upload required", 400)
				return
			}
			file, e := os.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
			if os.IsExist(e) {
				w.WriteHeader(412)
				return
			}
			if e != nil {
				http.Error(w, "fixture open failed", 500)
				return
			}
			started := time.Now()
			e = copySlow(r.Context(), file, r.Body, &uploaded)
			putNanos.Add(int64(time.Since(started)))
			if e == nil {
				e = file.Sync()
			}
			closeErr := file.Close()
			if e == nil {
				e = closeErr
			}
			if e != nil {
				_ = os.Remove(name)
				http.Error(w, "fixture transfer failed", 500)
				return
			}
			w.WriteHeader(200)
		case http.MethodGet:
			file, e := os.Open(name)
			if os.IsNotExist(e) {
				http.NotFound(w, r)
				return
			}
			if e != nil {
				http.Error(w, "fixture read failed", 500)
				return
			}
			defer file.Close()
			info, e := file.Stat()
			if e != nil {
				http.Error(w, "fixture stat failed", 500)
				return
			}
			w.Header().Set("Content-Type", "application/octet-stream")
			w.Header().Set("Content-Length", fmt.Sprint(info.Size()))
			started := time.Now()
			_ = copySlow(r.Context(), w, file, &downloaded)
			getNanos.Add(int64(time.Since(started)))
		default:
			http.Error(w, "unexpected fixture method", 405)
		}
	}))
	defer remote.Close()
	st, e := store.Open(filepath.Join(dataDir, "relaylab.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	vault, e := security.LoadVault(dataDir, false)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB.Exec("CREATE TABLE resource_fixture (id INTEGER PRIMARY KEY, payload BLOB NOT NULL)"); e != nil {
		t.Fatal(e)
	}
	tx, e := st.DB.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	statement, e := tx.PrepareContext(ctx, "INSERT INTO resource_fixture(payload) VALUES(?)")
	if e != nil {
		_ = tx.Rollback()
		t.Fatal(e)
	}
	chunk := make([]byte, 256<<10)
	for written := int64(0); written < payloadSize; written += int64(len(chunk)) {
		if _, e = rand.Read(chunk); e != nil {
			_ = statement.Close()
			_ = tx.Rollback()
			t.Fatal(e)
		}
		if _, e = statement.ExecContext(ctx, chunk); e != nil {
			_ = statement.Close()
			_ = tx.Rollback()
			t.Fatal(e)
		}
	}
	if e = statement.Close(); e != nil {
		_ = tx.Rollback()
		t.Fatal(e)
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	chunk = nil
	if _, e = st.DB.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB.Exec("PRAGMA shrink_memory"); e != nil {
		t.Fatal(e)
	}
	app := New(st, vault)
	host, _ := url.Parse(remote.URL)
	manager, e := backups.New(backups.Options{Store: st, Vault: vault, DataDir: dataDir, BuildVersion: "resource-fixture", Enter: app.EnterBackupActivity, AllowedStorageHosts: []string{host.Hostname()}})
	if e != nil {
		t.Fatal(e)
	}
	defer manager.Close()
	app.ConfigureBackups(ctx, manager, dataDir)
	public := httptest.NewServer(app.Handler())
	defer public.Close()
	identity, recipient, e := backup.GenerateIdentity()
	if e != nil {
		t.Fatal(e)
	}
	if e = manager.SetRecipient(ctx, recipient); e != nil {
		t.Fatal(e)
	}
	if e = manager.ValidateIdentity(ctx, identity); e != nil {
		t.Fatal(e)
	}
	identity = ""
	if _, e = manager.SaveStorage(ctx, backups.StorageInput{Endpoint: remote.URL, Region: "auto", Bucket: "resource-fixture", Prefix: "resource", ForcePathStyle: true, AccessKeyID: "resource-access-fixture", SecretAccessKey: "resource-secret-fixture"}); e != nil {
		t.Fatal(e)
	}
	policy, e := st.BackupPolicy(ctx)
	if e != nil {
		t.Fatal(e)
	}
	policy.Mode = "local_s3"
	if _, e = manager.SaveSchedule(ctx, policy); e != nil {
		t.Fatal(e)
	}
	runtime.GC()
	var baseline runtime.MemStats
	runtime.ReadMemStats(&baseline)
	var peak atomic.Uint64
	peak.Store(baseline.HeapAlloc)
	var maxRuntimeSys atomic.Uint64
	maxRuntimeSys.Store(baseline.Sys)
	var latencyMu sync.Mutex
	latencies := []float64{}
	var failures atomic.Int64
	stop := make(chan struct{})
	var observers sync.WaitGroup
	observers.Add(2)
	go func() {
		defer observers.Done()
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				var current runtime.MemStats
				runtime.ReadMemStats(&current)
				for old := peak.Load(); current.HeapAlloc > old && !peak.CompareAndSwap(old, current.HeapAlloc); old = peak.Load() {
				}
				for old := maxRuntimeSys.Load(); current.Sys > old && !maxRuntimeSys.CompareAndSwap(old, current.Sys); old = maxRuntimeSys.Load() {
				}
			}
		}
	}()
	go func() {
		defer observers.Done()
		client := &http.Client{Timeout: 12 * time.Second}
		ticker := time.NewTicker(250 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				for _, path := range []string{"/healthz", "/api/v1/public/monitor"} {
					start := time.Now()
					response, e := client.Get(public.URL + path)
					elapsed := time.Since(start).Seconds()
					latencyMu.Lock()
					latencies = append(latencies, elapsed)
					latencyMu.Unlock()
					if e != nil {
						failures.Add(1)
						continue
					}
					_, _ = io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
					if response.StatusCode != 200 {
						failures.Add(1)
					}
				}
			}
		}
	}()
	started := time.Now()
	run, e := manager.Create(ctx, "resource-96m-"+domain.NewID())
	if e != nil {
		close(stop)
		observers.Wait()
		t.Fatal(e)
	}
	for {
		run, e = manager.Record(ctx, run.ID)
		if e != nil {
			break
		}
		if run.State == "succeeded" || run.State == "failed" || run.State == "interrupted" {
			break
		}
		select {
		case <-ctx.Done():
			e = ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
		if e != nil {
			break
		}
	}
	elapsed := time.Since(started).Seconds()
	close(stop)
	observers.Wait()
	if e != nil {
		t.Fatal(e)
	}
	if run.State != "succeeded" || run.LocalState != "verified" || run.RemoteState != "verified" {
		t.Fatalf("resource backup failed: state=%s code=%s", run.State, run.ErrorCode)
	}
	file, e := os.Open(objectPath("/resource-fixture/" + run.ObjectKey))
	if e != nil {
		t.Fatal(e)
	}
	hash := sha256.New()
	remoteSize, e := io.Copy(hash, file)
	_ = file.Close()
	if e != nil || remoteSize != run.SizeBytes || hex.EncodeToString(hash.Sum(nil)) != run.SHA256 {
		t.Fatal("completed remote hash did not match")
	}
	dbInfo, e := os.Stat(filepath.Join(dataDir, "relaylab.db"))
	if e != nil {
		t.Fatal(e)
	}
	sort.Float64s(latencies)
	if len(latencies) == 0 {
		t.Fatal("no concurrent HTTP measurements")
	}
	p95 := latencies[(len(latencies)*95+99)/100-1]
	maximum := latencies[len(latencies)-1]
	delta := uint64(0)
	if peak.Load() > baseline.HeapAlloc {
		delta = peak.Load() - baseline.HeapAlloc
	}
	metrics := struct {
		PayloadBytes, DatabaseBytes, CipherBytes                                  int64
		ElapsedSeconds                                                            float64
		BaselineHeapBytes, PeakHeapBytes, PeakHeapDeltaBytes, PeakRuntimeSysBytes uint64
		HTTPProbeSamples                                                          int
		HTTPFailures                                                              int64
		HTTPP95Seconds, HTTPMaxSeconds                                            float64
		ConfiguredNetworkBytesPerSecond, UploadedBytes, DownloadedBytes           int64
		UploadSeconds, ReadbackSeconds                                            float64
		State                                                                     string
		Provider                                                                  string
	}{payloadSize, dbInfo.Size(), run.SizeBytes, elapsed, baseline.HeapAlloc, peak.Load(), delta, maxRuntimeSys.Load(), len(latencies), failures.Load(), p95, maximum, networkRate, uploaded.Load(), downloaded.Load(), time.Duration(putNanos.Load()).Seconds(), time.Duration(getNanos.Load()).Seconds(), run.State, "local disk-backed S3 fixture; not R2/AWS acceptance"}
	report := os.Getenv("RELAYLAB_BACKUP_RESOURCE_REPORT")
	if report == "" {
		repo, _ := filepath.Abs(filepath.Join("..", ".."))
		report = filepath.Join(repo, "release", "backup-audit-20261030", "resources.json")
	}
	if e = os.MkdirAll(filepath.Dir(report), 0700); e != nil {
		t.Fatal(e)
	}
	encoded, _ := json.MarshalIndent(metrics, "", "  ")
	if e = os.WriteFile(report, append(encoded, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
	t.Logf("96 MiB slow-storage backup completed in %.2fs; heap delta %.2f MiB; %d probes, p95 %.3fs, max %.3fs; report %s", elapsed, float64(delta)/(1<<20), len(latencies), p95, maximum, report)
	if delta >= 96<<20 {
		t.Errorf("heap delta exceeded bounded-memory guard: %d", delta)
	}
	if maximum >= 10 || failures.Load() != 0 {
		t.Errorf("concurrent HTTP regression: max %.3fs, failures %d", maximum, failures.Load())
	}
}
