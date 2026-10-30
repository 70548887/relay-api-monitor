package backups

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func TestS3WriteReadDeleteAndBackupRetry(t *testing.T) {
	var mu sync.Mutex
	objects := map[string][]byte{}
	corrupt := false
	puts := 0
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Error("missing AWS signature")
			http.Error(w, "denied", 403)
			return
		}
		key := r.URL.Path
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case "PUT":
			if !strings.Contains(key, ".connection-test-") && r.Header.Get("If-None-Match") != "*" {
				t.Error("backup upload must be conditional and refuse overwrites")
			}
			b, e := io.ReadAll(io.LimitReader(r.Body, 10<<20))
			if e != nil {
				http.Error(w, "read", 500)
				return
			}
			objects[key] = b
			puts++
			w.WriteHeader(200)
		case "GET":
			b, ok := objects[key]
			if !ok {
				http.NotFound(w, r)
				return
			}
			if corrupt && !strings.Contains(key, ".connection-test-") {
				b = append([]byte("changed"), b...)
			}
			w.Write(b)
		case "DELETE":
			delete(objects, key)
			w.WriteHeader(204)
		default:
			http.Error(w, "unexpected", 400)
		}
	}))
	defer fixture.Close()
	m, _ := newTestManager(t)
	u, _ := url.Parse(fixture.URL)
	m.opts.AllowedStorageHosts = []string{u.Hostname()}
	ctx := context.Background()
	in := StorageInput{Endpoint: fixture.URL, Region: "auto", Bucket: "relaylab-backups", Prefix: "fixture", AccessKeyID: "test-access", SecretAccessKey: "test-secret", ForcePathStyle: true}
	if e := m.TestStorage(ctx, in); e != nil {
		t.Fatal(e)
	}
	mu.Lock()
	if len(objects) != 0 {
		t.Fatal("connection test left objects")
	}
	corrupt = true
	mu.Unlock()
	if _, e := m.SaveStorage(ctx, in); e != nil {
		t.Fatal(e)
	}
	p, _ := m.opts.Store.BackupPolicy(ctx)
	p.Mode = "local_s3"
	if _, e := m.SaveSchedule(ctx, p); e != nil {
		t.Fatal(e)
	}
	r, e := m.Create(ctx, "s3-retry-test")
	if e != nil {
		t.Fatal(e)
	}
	r = waitRun(t, m, r.ID)
	if r.State != "failed" || r.LocalState != "verified" || r.RemoteState != "failed" {
		t.Fatalf("corruption accepted %+v", r)
	}
	hash := r.SHA256
	mu.Lock()
	corrupt = false
	mu.Unlock()
	retried, e := m.RetryUpload(ctx, r.ID)
	if e != nil {
		t.Fatal(e)
	}
	retried = waitRun(t, m, retried.ID)
	if retried.State != "succeeded" || retried.RemoteState != "verified" || retried.SHA256 != hash {
		t.Fatalf("retry %+v", retried)
	}
	records, e := m.List(ctx, 100, 0)
	if e != nil || len(records) != 1 {
		t.Fatalf("retry created new snapshot %d %v", len(records), e)
	}
	mu.Lock()
	defer mu.Unlock()
	if puts != 2 {
		t.Fatalf("expected one connection test and one upload; retry must read the existing object, got %d writes", puts)
	}
}
