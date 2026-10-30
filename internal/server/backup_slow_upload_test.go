package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"relaylab/internal/backup"
	"relaylab/internal/domain"
	"relaylab/internal/restorestate"
)

// This opt-in regression deliberately takes more than thirty real seconds.
// It exercises the production handler over TCP with the normal server read
// timeout; small or simulated deadlines would not prove the upload override.
func TestBackupSlowUploadSurvivesDefaultHTTPReadTimeout(t *testing.T) {
	if os.Getenv("RELAYLAB_SLOW_UPLOAD_TEST") != "1" {
		t.Skip("set RELAYLAB_SLOW_UPLOAD_TEST=1 to run the real 31-second upload")
	}
	s, handler, _, dataDir := backupTestApp(t)
	cookie, csrf := loginForTest(t, handler)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	if e := s.Store.SavePlatform(ctx, domain.Platform{ID: "slow-upload-marker", Name: "unchanged business data"}); e != nil {
		t.Fatal(e)
	}
	var before string
	if e := s.Store.DB.QueryRowContext(ctx, "SELECT data FROM platforms WHERE id='slow-upload-marker'").Scan(&before); e != nil {
		t.Fatal(e)
	}
	keyBefore, e := os.ReadFile(filepath.Join(dataDir, "master.key"))
	if e != nil {
		t.Fatal(e)
	}
	keyDigest := sha256.Sum256(keyBefore)
	clear(keyBefore)

	uploadEntered := make(chan struct{})
	var enterOnce sync.Once
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/admin/backups/restore/preflight" {
			enterOnce.Do(func() { close(uploadEntered) })
		}
		handler.ServeHTTP(w, r)
	}))
	server.Config.ReadHeaderTimeout = 10 * time.Second
	server.Config.ReadTimeout = 30 * time.Second
	server.Start()
	defer server.Close()
	client := server.Client()
	client.Timeout = 45 * time.Second
	newRequest := func(path string, body io.Reader) *http.Request {
		req, e := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+path, body)
		if e != nil {
			t.Fatal(e)
		}
		req.AddCookie(cookie)
		req.Header.Set("Origin", server.URL)
		req.Header.Set("X-CSRF-Token", csrf)
		return req
	}
	verify := newRequest("/api/v1/admin/backups/reauth", bytes.NewBufferString(`{"password":"password-test-123456"}`))
	verify.Header.Set("Content-Type", "application/json")
	response, e := client.Do(verify)
	if e != nil {
		t.Fatal(e)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("reauth status %d", response.StatusCode)
	}

	identity, recipient, e := backup.GenerateIdentity()
	if e != nil {
		t.Fatal(e)
	}
	input := filepath.Join(t.TempDir(), "slow.zip.age")
	if e = backup.CreateFromStore(ctx, s.Store, dataDir, input, backup.Options{Recipient: recipient, Schema: 4}); e != nil {
		t.Fatal(e)
	}
	archive, e := os.ReadFile(input)
	if e != nil {
		t.Fatal(e)
	}
	var wire bytes.Buffer
	form := multipart.NewWriter(&wire)
	if e = form.WriteField("identity", identity); e != nil {
		t.Fatal(e)
	}
	part, e := form.CreateFormFile("file", "slow.zip.age")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = part.Write(archive); e != nil {
		t.Fatal(e)
	}
	if e = form.Close(); e != nil {
		t.Fatal(e)
	}
	identity = ""
	content := wire.Bytes()
	archiveOffset := bytes.Index(content, archive)
	if archiveOffset < 0 || len(archive) < 2 {
		t.Fatal("invalid multipart fixture")
	}
	split := archiveOffset + len(archive)/2
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	writeDone := make(chan error, 1)
	go func() {
		var writeErr error
		defer func() { _ = writer.CloseWithError(writeErr); writeDone <- writeErr }()
		if _, writeErr = writer.Write(content[:split]); writeErr != nil {
			return
		}
		select {
		case <-uploadEntered:
		case <-ctx.Done():
			writeErr = ctx.Err()
			return
		}
		timer := time.NewTimer(31 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
			writeErr = ctx.Err()
			return
		}
		_, writeErr = writer.Write(content[split:])
	}()
	started := time.Now()
	upload := newRequest("/api/v1/admin/backups/restore/preflight", reader)
	upload.Header.Set("Content-Type", form.FormDataContentType())
	response, e = client.Do(upload)
	if e != nil {
		cancel()
		_ = reader.CloseWithError(e)
		<-writeDone
		t.Fatal("slow upload failed", e)
	}
	var result struct {
		ID       string `json:"id"`
		Manifest struct {
			DatabaseSchema int `json:"database_schema"`
		} `json:"manifest"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	decodeErr := json.NewDecoder(io.LimitReader(response.Body, 128<<10)).Decode(&result)
	_ = response.Body.Close()
	if response.StatusCode != 200 {
		cancel()
		_ = reader.Close()
		<-writeDone
		t.Fatalf("slow upload status %d code %s", response.StatusCode, result.Error.Code)
	}
	if decodeErr != nil || result.ID == "" || result.Manifest.DatabaseSchema != 4 {
		t.Fatal("preflight response invalid", decodeErr)
	}
	if e = <-writeDone; e != nil {
		t.Fatal("multipart writer failed", e)
	}
	elapsed := time.Since(started)
	if elapsed < 31*time.Second {
		t.Fatal("test did not exceed production read timeout")
	}
	var preflight restorestate.Preflight
	if e = restorestate.ReadPreflight(dataDir, result.ID, &preflight); e != nil || !time.Now().Before(preflight.ExpiresAt) {
		t.Fatal("preflight was not saved", e)
	}
	var after string
	if e = s.Store.DB.QueryRowContext(ctx, "SELECT data FROM platforms WHERE id='slow-upload-marker'").Scan(&after); e != nil || after != before {
		t.Fatal("preflight modified business data", e)
	}
	keyAfter, e := os.ReadFile(filepath.Join(dataDir, "master.key"))
	if e != nil {
		t.Fatal(e)
	}
	if sha256.Sum256(keyAfter) != keyDigest {
		t.Fatal("preflight modified master key")
	}
	clear(keyAfter)
	check, e := http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/api/v1/auth/me", nil)
	if e != nil {
		t.Fatal(e)
	}
	check.AddCookie(cookie)
	response, e = client.Do(check)
	if e != nil {
		t.Fatal(e)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatal("preflight invalidated current session", response.StatusCode)
	}
	t.Log(fmt.Sprintf("PASS real HTTP upload %.2fs with ReadTimeout=30s: preflight Schema4 saved; business marker, master key and session unchanged", elapsed.Seconds()))
}
