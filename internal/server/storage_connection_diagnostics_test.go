package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"relaylab/internal/backups"
	"relaylab/internal/domain"
)

func TestStorageConnectionResultAuditsSuccessAndFailureWithoutSecrets(t *testing.T) {
	var denied atomic.Bool
	var mu sync.Mutex
	objects := map[string][]byte{}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if denied.Load() {
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(403)
			_, _ = io.WriteString(w, "<Error><Code>AccessDenied</Code><Message>fixture-secret-do-not-log fixture-access-do-not-log</Message></Error>")
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case "PUT":
			objects[r.URL.Path], _ = io.ReadAll(r.Body)
			w.WriteHeader(200)
		case "GET":
			_, _ = w.Write(objects[r.URL.Path])
		case "DELETE":
			delete(objects, r.URL.Path)
			w.WriteHeader(204)
		default:
			w.WriteHeader(405)
		}
	}))
	defer fixture.Close()
	s, h, _, dir := backupTestApp(t)
	u, _ := url.Parse(fixture.URL)
	m, err := backups.New(backups.Options{Store: s.Store, Vault: s.Vault, DataDir: dir, AllowedStorageHosts: []string{u.Hostname()}})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	s.backupHTTP.manager = m
	cookie, csrf := loginForTest(t, h)
	input := backups.StorageInput{Endpoint: fixture.URL, Region: "auto", Bucket: "fixture-bucket", Prefix: "fixture-prefix", ForcePathStyle: true, AccessKeyID: "fixture-access-do-not-log", SecretAccessKey: "fixture-secret-do-not-log"}
	if _, err = m.SaveStorage(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	var output bytes.Buffer
	previousLogger := log.Writer()
	log.SetOutput(&output)
	defer log.SetOutput(previousLogger)
	for _, failRequest := range []bool{false, true} {
		denied.Store(failRequest)
		w := request(t, h, "POST", "/api/v1/admin/backups/storage/test", input, cookie, csrf)
		var response struct {
			OK     bool                           `json:"ok"`
			Result domain.BackupStorageTestResult `json:"result"`
			Error  struct{ Code, Message string } `json:"error"`
		}
		if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.Result.RequestID == "" || response.Result.RequestID != w.Header().Get("X-Request-ID") {
			t.Fatal("response lacks server-generated diagnostic ID")
		}
		if response.Result.TestScope != "saved" || response.Result.StorageID == "" || response.Result.StorageRevision < 1 {
			t.Fatal("response lacks server-verified storage revision")
		}
		if failRequest {
			if w.Code != 502 || response.Result.Stage != "writing" || response.Result.HTTPStatus != 403 || response.Error.Code != "permission_denied" || response.Result.CleanupErrorCode == "" {
				t.Fatalf("missing failure/cleanup diagnostics: %d %+v", w.Code, response)
			}
			if me := request(t, h, "GET", "/api/v1/auth/me", nil, cookie, ""); me.Code != 200 {
				t.Fatal("upstream authentication error revoked local administrator session")
			}
		} else if w.Code != 200 || !response.OK || response.Result.Status != "ok" || response.Result.Stage != "completed" {
			t.Fatalf("success response invalid: %d %+v", w.Code, response)
		}
		for _, forbidden := range []string{input.AccessKeyID, input.SecretAccessKey, fixture.URL, ".connection-test-"} {
			if strings.Contains(w.Body.String(), forbidden) || strings.Contains(output.String(), forbidden) {
				t.Fatal("sensitive connection detail in API or log")
			}
		}
	}
	events, err := s.Store.ListAudit(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	count, failures := 0, 0
	for _, event := range events {
		if event.Action != "backup_storage_test" {
			continue
		}
		count++
		var result domain.BackupStorageTestResult
		if json.Unmarshal([]byte(event.Detail), &result) != nil || result.RequestID != event.EntityID {
			t.Fatal("audit result missing correlation")
		}
		if result.Status == "failed" {
			failures++
		}
		for _, secret := range []string{input.AccessKeyID, input.SecretAccessKey, fixture.URL} {
			if strings.Contains(event.Detail, secret) {
				t.Fatal("audit contains private connection data")
			}
		}
	}
	if count != 2 || failures != 1 || strings.Count(output.String(), "backup_storage_test ") != 2 {
		t.Fatal("success/failure diagnostics not both recorded", count, failures)
	}
}

func TestStorageConnectionDiagnosticsRemainAdminAndCSRFGated(t *testing.T) {
	s, h, _, _ := backupTestApp(t)
	cookie, csrf := loginForTest(t, h)
	for _, tc := range []struct {
		cookie *http.Cookie
		csrf   string
		want   int
	}{{nil, "", 401}, {cookie, "", 403}} {
		if w := request(t, h, "POST", "/api/v1/admin/backups/storage/test", map[string]string{}, tc.cookie, tc.csrf); w.Code != tc.want {
			t.Fatal("connection test authorization changed", w.Code)
		}
	}
	w := request(t, h, "POST", "/api/v1/admin/backups/storage/test", map[string]string{"endpoint": "invalid"}, cookie, csrf)
	if w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_storage_config") || !strings.Contains(w.Body.String(), "\"stage\":\"validating\"") {
		t.Fatal("validation error lacks safe diagnostics", w.Code, w.Body.String())
	}
	events, err := s.Store.ListAudit(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, event := range events {
		if event.Action == "backup_storage_test" {
			count++
		}
	}
	if count != 1 {
		t.Fatal("only authorized execution must be audited as a connection test", count)
	}
}
