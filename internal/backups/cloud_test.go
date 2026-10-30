package backups

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"relaylab/internal/durablejson"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCloudDirectoryImportValidatesAndPreservesRemoteSource(t *testing.T) {
	source, identity := newTestManager(t)
	ctx := context.Background()
	run, e := source.Create(ctx, "cloud-source-fixture")
	if e != nil {
		t.Fatal(e)
	}
	run = waitRun(t, source, run.ID)
	run, e = source.Record(ctx, run.ID)
	if e != nil {
		t.Fatal(e)
	}
	payload, e := os.ReadFile(run.LocalPath)
	if e != nil {
		t.Fatal(e)
	}
	key := "backups/remote-instance/2026/10/" + run.ID + "/" + run.FileName
	var gets, deletes, lists atomic.Int32
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") {
			t.Error("unsigned request")
			w.WriteHeader(403)
			return
		}
		if r.Method == "GET" && r.URL.Query().Get("list-type") == "2" {
			lists.Add(1)
			if r.URL.Query().Get("prefix") != "backups/" {
				t.Error("prefix not bounded")
			}
			type item struct {
				Key      string `xml:"Key"`
				Size     int    `xml:"Size"`
				ETag     string `xml:"ETag"`
				Modified string `xml:"LastModified"`
			}
			type listing struct {
				XMLName   xml.Name `xml:"ListBucketResult"`
				Items     []item   `xml:"Contents"`
				Truncated bool     `xml:"IsTruncated"`
				Next      string   `xml:"NextContinuationToken,omitempty"`
			}
			out := listing{}
			if r.URL.Query().Get("continuation-token") == "" {
				out.Items = []item{{key, len(payload), `"fixture-etag"`, time.Now().UTC().Format(time.RFC3339)}}
				out.Truncated = true
				out.Next = "next-page"
			}
			w.Header().Set("Content-Type", "application/xml")
			_ = xml.NewEncoder(w).Encode(out)
			return
		}
		if r.Method == "GET" && r.URL.Path == "/fixture-bucket/"+key {
			gets.Add(1)
			if r.Header.Get("If-Match") != `"fixture-etag"` {
				t.Error("object version not pinned")
			}
			_, _ = w.Write(payload)
			return
		}
		if r.Method == "DELETE" {
			deletes.Add(1)
			w.WriteHeader(204)
			return
		}
		http.NotFound(w, r)
	}))
	defer fixture.Close()
	m, _ := newTestManager(t)
	u, _ := url.Parse(fixture.URL)
	m.opts.AllowedStorageHosts = []string{u.Hostname()}
	if _, e = m.SaveStorage(ctx, StorageInput{Endpoint: fixture.URL, Region: "auto", Bucket: "fixture-bucket", Prefix: "backups", AccessKeyID: "cloud-fixture-access", SecretAccessKey: "cloud-fixture-secret", ForcePathStyle: true}); e != nil {
		t.Fatal(e)
	}
	page, e := m.ScanCloud(ctx, "")
	if e != nil || len(page.Items) != 1 || !page.Truncated || page.ContinuationToken != "next-page" {
		t.Fatalf("scan %+v %v", page, e)
	}
	encoded, _ := json.Marshal(page)
	if strings.Contains(string(encoded), key) || strings.Contains(string(encoded), "cloud-fixture-secret") {
		t.Fatal("private registration exposed")
	}
	page2, e := m.ScanCloud(ctx, page.ContinuationToken)
	if e != nil || page2.Truncated || len(page2.Items) != 0 {
		t.Fatal("pagination", page2, e)
	}
	if _, e = m.ImportCloud(ctx, page.Items[0].ID, "wrong"); e == nil {
		t.Fatal("wrong key imported")
	}
	if rows, _ := m.List(ctx, 100, 0); len(rows) != 0 {
		t.Fatal("failed validation created run")
	}
	imported, e := m.ImportCloud(ctx, page.Items[0].ID, identity)
	if e != nil {
		t.Fatal(e)
	}
	if !imported.CloudImported || !imported.Protected || imported.State != "succeeded" || imported.RemoteState != "verified" {
		t.Fatal(imported)
	}
	if imported.SHA256 != run.SHA256 || imported.SizeBytes != run.SizeBytes {
		t.Fatal("source integrity changed")
	}
	before := gets.Load()
	again, e := m.ImportCloud(ctx, page.Items[0].ID, identity)
	if e != nil || again.ID != imported.ID || gets.Load() != before {
		t.Fatal("duplicate import fetched or duplicated", e)
	}
	page, e = m.ScanCloud(ctx, "")
	if e != nil || !page.Items[0].Imported {
		t.Fatal("scan not marked imported", e)
	}
	if e = m.Protect(ctx, imported.ID, false); e != nil {
		t.Fatal(e)
	}
	if e = m.Delete(ctx, imported.ID); e != nil {
		t.Fatal(e)
	}
	if deletes.Load() != 0 {
		t.Fatal("import deleted remote source")
	}
	if lists.Load() != 3 {
		t.Fatal(lists.Load())
	}
}

func TestCloudImportOnlyAcceptsRegisteredUnexpiredCandidates(t *testing.T) {
	m, _ := newTestManager(t)
	ctx := context.Background()
	if _, e := m.ImportCloud(ctx, "../escape", "key"); e == nil {
		t.Fatal("raw path accepted")
	}
	id := strings.Repeat("a", 32)
	reg := cloudRegistration{Candidate: CloudCandidate{ID: id}, ExpiresAt: time.Now().Add(-time.Minute)}
	if e := os.MkdirAll(m.cloudRoot(), 0700); e != nil {
		t.Fatal(e)
	}
	if e := durablejson.Write(m.cloudRegistrationPath(id), reg); e != nil {
		t.Fatal(e)
	}
	if _, e := m.ImportCloud(ctx, id, "key"); e == nil {
		t.Fatal("expired registration imported")
	}
}

func TestCloudListResponseBodyIsBounded(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, strings.Repeat("x", (1<<20)+4096))
	}))
	defer server.Close()
	u, _ := url.Parse(server.URL)
	client := storageHTTP([]string{u.Hostname()})
	response, e := client.Get(server.URL + "?list-type=2")
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	b, e := io.ReadAll(response.Body)
	if e != nil || len(b) != 1<<20 {
		t.Fatal("list response unbounded", len(b), e)
	}
}
