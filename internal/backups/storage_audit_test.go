package backups

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func storageAuditInput(endpoint string) StorageInput {
	return StorageInput{Endpoint: endpoint, Region: "auto", Bucket: "relaylab-audit", Prefix: "audit", AccessKeyID: "audit-access-credential", SecretAccessKey: "audit-secret-credential-never-public", ForcePathStyle: true}
}

func allowStorageFixture(t *testing.T, m *Manager, endpoint string) {
	t.Helper()
	u, e := url.Parse(endpoint)
	if e != nil {
		t.Fatal(e)
	}
	m.opts.AllowedStorageHosts = []string{u.Hostname()}
}

func TestStorageExistingDifferentObjectNeverOverwrittenAndHealthIsSanitized(t *testing.T) {
	var writes atomic.Int32
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "GET":
			_, _ = io.WriteString(w, "preexisting-other-backup-object")
		case "PUT":
			writes.Add(1)
			t.Error("different existing object must never be overwritten")
			w.WriteHeader(200)
		default:
			w.WriteHeader(405)
		}
	}))
	defer fixture.Close()
	m, _ := newTestManager(t)
	allowStorageFixture(t, m, fixture.URL)
	ctx := context.Background()
	in := storageAuditInput(fixture.URL)
	storage, e := m.SaveStorage(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	p, e := m.opts.Store.BackupPolicy(ctx)
	if e != nil {
		t.Fatal(e)
	}
	p.Mode = "local_s3"
	if _, e = m.SaveSchedule(ctx, p); e != nil {
		t.Fatal(e)
	}
	r, e := m.Create(ctx, "existing-object-conflict")
	if e != nil {
		t.Fatal(e)
	}
	r = waitRun(t, m, r.ID)
	if r.State != "failed" || r.LocalState != "verified" || r.RemoteState != "failed" || writes.Load() != 0 {
		t.Fatalf("unexpected conflict result %+v writes=%d", r, writes.Load())
	}
	if _, e = m.RetryUpload(ctx, r.ID); e != nil {
		t.Fatal(e)
	}
	r = waitRun(t, m, r.ID)
	if r.State != "failed" || writes.Load() != 0 {
		t.Fatal("retry overwrote an existing object", writes.Load(), r.State)
	}
	saved, _, e := m.opts.Store.BackupStorage(ctx, storage.ID)
	if e != nil {
		t.Fatal(e)
	}
	if saved.Health.Status != "failed" || saved.Health.CheckedAt == nil || saved.Health.Operation != "upload" {
		t.Fatalf("missing failure health %+v", saved.Health)
	}
	public, e := json.Marshal(saved)
	if e != nil {
		t.Fatal(e)
	}
	for _, secret := range []string{in.AccessKeyID, in.SecretAccessKey, "preexisting-other-backup-object"} {
		if strings.Contains(string(public), secret) {
			t.Fatal("public storage metadata leaked a credential or raw upstream response")
		}
	}
}

func TestStorageConnectionHealthMatchesOnlySavedConfiguration(t *testing.T) {
	var deny atomic.Bool
	var mu sync.Mutex
	var payload string
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if deny.Load() {
			http.Error(w, "denied audit-secret-credential-never-public audit-access-credential", 403)
			return
		}
		switch r.Method {
		case "PUT":
			b, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
			payload = string(b)
			w.WriteHeader(200)
		case "GET":
			_, _ = io.WriteString(w, payload)
		case "DELETE":
			payload = ""
			w.WriteHeader(204)
		default:
			w.WriteHeader(405)
		}
	}))
	defer fixture.Close()
	m, _ := newTestManager(t)
	allowStorageFixture(t, m, fixture.URL)
	ctx := context.Background()
	in := storageAuditInput(fixture.URL)
	first, e := m.SaveStorage(ctx, in)
	if e != nil {
		t.Fatal(e)
	}
	if first.Health.Status != "untested" {
		t.Fatal("new configuration is not yet tested")
	}
	withoutCredentials := in
	withoutCredentials.AccessKeyID = ""
	withoutCredentials.SecretAccessKey = ""
	if e = m.TestStorage(ctx, withoutCredentials); e != nil {
		t.Fatal(e)
	}
	saved, _, e := m.opts.Store.BackupStorage(ctx, first.ID)
	if e != nil {
		t.Fatal(e)
	}
	if saved.Health.Status != "ok" || saved.Health.CheckedAt == nil {
		t.Fatalf("successful test not recorded %+v", saved.Health)
	}
	second, e := m.SaveStorage(ctx, withoutCredentials)
	if e != nil {
		t.Fatal(e)
	}
	if second.ID == first.ID || second.Health.Status != "untested" {
		t.Fatal("storage revision must not inherit a previous revision's health")
	}
	deny.Store(true)
	if e = m.TestStorage(ctx, withoutCredentials); e == nil {
		t.Fatal("denied connection test should fail")
	} else if strings.Contains(e.Error(), in.SecretAccessKey) || strings.Contains(e.Error(), in.AccessKeyID) {
		t.Fatal("connection error leaked raw credentials")
	}
	saved, _, e = m.opts.Store.BackupStorage(ctx, second.ID)
	if e != nil {
		t.Fatal(e)
	}
	if saved.Health.Status != "failed" {
		t.Fatalf("failure not recorded %+v", saved.Health)
	}
	old, _, e := m.opts.Store.BackupStorage(ctx, first.ID)
	if e != nil {
		t.Fatal(e)
	}
	if old.Health.Status != "ok" {
		t.Fatal("testing current revision changed old revision health")
	}
	public, _ := json.Marshal(saved)
	if strings.Contains(string(public), in.SecretAccessKey) || strings.Contains(string(public), in.AccessKeyID) {
		t.Fatal("failure health exposes credentials")
	}
}

func TestStorageConditionalUploadConflictAcceptsOnlyMatchingObject(t *testing.T) {
	var writes atomic.Int32
	var mu sync.Mutex
	var object []byte
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case "GET":
			if object == nil {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(object)
		case "PUT":
			writes.Add(1)
			if r.Header.Get("If-None-Match") != "*" {
				t.Error("upload must require an absent key")
			}
			object, _ = io.ReadAll(io.LimitReader(r.Body, 10<<20))
			// A race or lost acknowledgement can make a conditional upload fail
			// although the exact object has arrived. The following read is decisive.
			http.Error(w, "conditional conflict", http.StatusPreconditionFailed)
		default:
			w.WriteHeader(405)
		}
	}))
	defer fixture.Close()
	m, _ := newTestManager(t)
	allowStorageFixture(t, m, fixture.URL)
	ctx := context.Background()
	if _, e := m.SaveStorage(ctx, storageAuditInput(fixture.URL)); e != nil {
		t.Fatal(e)
	}
	p, e := m.opts.Store.BackupPolicy(ctx)
	if e != nil {
		t.Fatal(e)
	}
	p.Mode = "local_s3"
	if _, e = m.SaveSchedule(ctx, p); e != nil {
		t.Fatal(e)
	}
	r, e := m.Create(ctx, "conditional-upload-race")
	if e != nil {
		t.Fatal(e)
	}
	r = waitRun(t, m, r.ID)
	if r.State != "succeeded" || r.RemoteState != "verified" || writes.Load() != 1 {
		t.Fatalf("matching conditional conflict not recovered %+v writes=%d", r, writes.Load())
	}
}

func TestStorageNetworkGuardsPublicAddressesAndExplicitLocalAllowlist(t *testing.T) {
	for _, text := range []string{"127.0.0.1", "10.1.2.3", "172.16.1.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "192.0.2.2", "198.18.0.2", "198.51.100.2", "203.0.113.2", "224.0.0.1", "240.0.0.1", "::1", "::ffff:127.0.0.1", "fc00::1", "fe80::1", "2001:db8::1"} {
		if publicAddress(netip.MustParseAddr(text)) {
			t.Errorf("reserved address accepted: %s", text)
		}
	}
	for _, text := range []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"} {
		if !publicAddress(netip.MustParseAddr(text)) {
			t.Errorf("public address rejected: %s", text)
		}
	}
	var calls atomic.Int32
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(204) }))
	defer fixture.Close()
	u, _ := url.Parse(fixture.URL)
	blocked := storageHTTP(nil)
	resp, e := blocked.Get(fixture.URL)
	if resp != nil {
		resp.Body.Close()
	}
	if e == nil || calls.Load() != 0 {
		t.Fatal("unapproved local endpoint reached network", e, calls.Load())
	}
	permitted := storageHTTP([]string{u.Hostname()})
	resp, e = permitted.Get(fixture.URL)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if calls.Load() != 1 {
		t.Fatal("explicit allowlist did not permit local fixture")
	}
	if permitted.Timeout <= 0 || permitted.Timeout > 30*time.Minute {
		t.Fatal("storage requests require a finite overall deadline")
	}
}

func TestStorageRedirectNeverForwardsCredentialsAndRequestCancellationStopsRead(t *testing.T) {
	var targetCalls atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { targetCalls.Add(1); w.WriteHeader(204) }))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()
	u, _ := url.Parse(source.URL)
	client := storageHTTP([]string{u.Hostname()})
	req, _ := http.NewRequest("GET", source.URL, nil)
	req.Header.Set("Authorization", "AWS4-HMAC-SHA256 secret")
	resp, e := client.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	if e == nil || targetCalls.Load() != 0 {
		t.Fatal("redirect followed", e, targetCalls.Load())
	}
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer stalled.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req, _ = http.NewRequestWithContext(ctx, "GET", stalled.URL, nil)
	started := time.Now()
	resp, e = client.Do(req)
	if resp != nil {
		resp.Body.Close()
	}
	if e == nil || time.Since(started) > 2*time.Second {
		t.Fatal("canceled request did not stop", e, time.Since(started))
	}
}
