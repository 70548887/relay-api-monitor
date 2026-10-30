package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"relaylab/internal/backup"
	"relaylab/internal/backups"
	"relaylab/internal/domain"
	"relaylab/internal/restorestate"
	"relaylab/internal/security"
	"relaylab/internal/store"
)

func backupTestApp(t *testing.T) (*Server, http.Handler, *backups.Manager, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "data")
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	st, e := store.Open(filepath.Join(dir, "relaylab.db"))
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = st.Close() })
	v, e := security.LoadVault(dir, false)
	if e != nil {
		t.Fatal(e)
	}
	s := New(st, v)
	m, e := backups.New(backups.Options{Store: st, Vault: v, DataDir: dir, Enter: s.EnterBackupActivity, BuildVersion: "1.5.0"})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.ConfigureBackups(ctx, m, dir)
	m.Start(ctx)
	t.Cleanup(func() { cancel(); m.Close() })
	return s, s.Handler(), m, dir
}
func TestBackupAdminPermissionsAndReauthentication(t *testing.T) {
	s, h, m, _ := backupTestApp(t)
	if w := request(t, h, "GET", "/api/v1/admin/backups/status", nil, nil, ""); w.Code != 401 {
		t.Fatal(w.Code)
	}
	cookie, csrf := loginForTest(t, h)
	if w := request(t, h, "PUT", "/api/v1/admin/backups/keys/recipient", map[string]string{"recipient": "invalid"}, cookie, ""); w.Code != 403 {
		t.Fatal("csrf", w.Code)
	}
	if w := request(t, h, "POST", "/api/v1/admin/backups/reauth", map[string]string{"password": "wrong"}, cookie, csrf); w.Code != 403 {
		t.Fatal("reauth", w.Code)
	}
	if w := request(t, h, "POST", "/api/v1/admin/backups/restore/preflight", map[string]string{"record_id": "missing"}, cookie, csrf); w.Code != 403 {
		t.Fatal("unverified preflight", w.Code)
	}
	w := request(t, h, "POST", "/api/v1/admin/backups/keys/generate", map[string]any{}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var keys struct{ Identity, Recipient string }
	_ = json.Unmarshal(w.Body.Bytes(), &keys)
	w = request(t, h, "PUT", "/api/v1/admin/backups/keys/recipient", map[string]string{"recipient": keys.Recipient}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = request(t, h, "POST", "/api/v1/admin/backups/keys/verify", map[string]string{"identity": keys.Identity}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var run backups.Run
	w = request(t, h, "POST", "/api/v1/admin/backups/records", map[string]string{"request_id": "server-record-once"}, cookie, csrf)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body.String())
	}
	_ = json.Unmarshal(w.Body.Bytes(), &run)
	until := time.Now().Add(10 * time.Second)
	for time.Now().Before(until) {
		run, _ = m.Record(context.Background(), run.ID)
		if run.State == "succeeded" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if run.State != "succeeded" {
		t.Fatal(run)
	}
	w = request(t, h, "GET", "/api/v1/admin/backups/records/"+run.ID+"/download", nil, cookie, "")
	if w.Code != 403 {
		t.Fatal("download must reauth", w.Code)
	}
	w = request(t, h, "POST", "/api/v1/admin/backups/reauth", map[string]string{"password": "password-test-123456"}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	w = request(t, h, "GET", "/api/v1/admin/backups/records/"+run.ID+"/download", nil, cookie, "")
	if w.Code != 200 || !strings.Contains(w.Header().Get("Content-Disposition"), "attachment") {
		t.Fatal(w.Code)
	}
	w = request(t, h, "GET", "/api/v1/admin/backups/status", nil, cookie, "")
	if w.Code != 200 || strings.Contains(w.Body.String(), keys.Identity) || strings.Contains(w.Body.String(), "master.key") {
		t.Fatal("credential exposed", w.Body.String())
	}
	// A member session does not grant backup privileges.
	member, token, _ := registerMemberForAuthTest(t, s.Handler(), "backup-member")
	w = memberRequest(t, s, "GET", "/api/v1/admin/backups/status", nil, member, token)
	if w.Code != 401 {
		t.Fatal("member accessed backup", w.Code)
	}
}
func TestBackupMultipartPreflightExceedsGeneralLimitAndIsIndependent(t *testing.T) {
	s, h, _, dir := backupTestApp(t)
	cookie, csrf := loginForTest(t, h)
	w := request(t, h, "POST", "/api/v1/admin/backups/reauth", map[string]string{"password": "password-test-123456"}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	if _, e := s.Store.DB.Exec("CREATE TABLE backup_payload(data BLOB)"); e != nil {
		t.Fatal(e)
	}
	payload := make([]byte, 3<<20)
	if _, e := rand.Read(payload); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Store.DB.Exec("INSERT INTO backup_payload VALUES(?)", payload); e != nil {
		t.Fatal(e)
	}
	identity, recipient, e := backup.GenerateIdentity()
	if e != nil {
		t.Fatal(e)
	}
	input := filepath.Join(t.TempDir(), "import.zip.age")
	if e = backup.CreateFromStore(context.Background(), s.Store, dir, input, backup.Options{Recipient: recipient, Schema: 4}); e != nil {
		t.Fatal(e)
	}
	// An unknown, >2MiB multipart field must fail for schema reasons, not at the general body cap.
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("identity", identity)
	part, _ := mw.CreateFormFile("file", "import.zip.age")
	b, _ := os.ReadFile(input)
	if len(b) <= 2<<20 {
		t.Fatal("fixture must exceed general API body limit")
	}
	_, _ = part.Write(b)
	_ = mw.Close()
	req := httptest.NewRequest("POST", "/api/v1/admin/backups/restore/preflight", &buf)
	req.Host = "localhost"
	req.Header.Set("Origin", "http://localhost")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-CSRF-Token", csrf)
	req.AddCookie(cookie)
	out := httptest.NewRecorder()
	h.ServeHTTP(out, req)
	if out.Code != 200 {
		t.Fatal(out.Code, out.Body.String())
	}
	if strings.Contains(out.Body.String(), "restore-docker.sh") || !strings.Contains(out.Body.String(), "--data-dir") {
		t.Fatal("native deployment received Docker-only recovery steps")
	}
	var result struct {
		ID string `json:"id"`
	}
	_ = json.Unmarshal(out.Body.Bytes(), &result)
	var p restorestate.Preflight
	if e = restorestate.ReadPreflight(dir, result.ID, &p); e != nil {
		t.Fatal(e)
	}
	if !restorestate.Contained(restorestate.Stage(dir, result.ID), p.Dir) {
		t.Fatal("stage escaped")
	}
	w = request(t, h, "POST", "/api/v1/admin/backups/restore/apply", map[string]string{"preflight_id": result.ID}, cookie, csrf)
	if w.Code != 409 {
		t.Fatal("unmanaged apply", w.Code)
	}
	var count int
	if e = s.Store.DB.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&count); e != nil || count != 1 {
		t.Fatal("preflight invalidated active account", count, e)
	}
}
func TestRestoreStatusTokenOnlyReadsOneOperation(t *testing.T) {
	s, h, _, dir := backupTestApp(t)
	id := domain.NewID()
	token := security.Token()
	op := restorestate.Operation{ID: id, State: "succeeded", TokenHash: security.HashToken(token), TokenExpiresAt: time.Now().Add(time.Minute), SafetyDir: "private-path"}
	if e := restorestate.SaveOperation(dir, op); e != nil {
		t.Fatal(e)
	}
	s.SetMaintenance(true)
	req := httptest.NewRequest("GET", "/api/v1/admin/backups/operations/"+id, nil)
	req.Header.Set("X-Restore-Status-Token", token)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 200 || strings.Contains(w.Body.String(), "private-path") || strings.Contains(w.Body.String(), op.TokenHash) {
		t.Fatal(w.Code, w.Body.String())
	}
	req = httptest.NewRequest("GET", "/api/v1/admin/backups/operations/"+domain.NewID(), nil)
	req.Header.Set("X-Restore-Status-Token", token)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 404 {
		t.Fatal(w.Code)
	}
}
