package backups

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/smithy-go"
	"relaylab/internal/domain"
	"relaylab/internal/upstream"
)

func TestStorageDetailedTestCleansAmbiguousPutAndPreservesPrimaryFailure(t *testing.T) {
	var mu sync.Mutex
	objects := map[string][]byte{}
	putKeys := map[string]bool{}
	deletes := 0
	puts := 0
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case "PUT":
			puts++
			if _, alreadyWritten := objects[r.URL.Path]; alreadyWritten {
				w.WriteHeader(412)
				return
			}
			body, _ := io.ReadAll(r.Body)
			objects[r.URL.Path] = body
			putKeys[r.URL.Path] = true
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(503)
			_, _ = io.WriteString(w, "<Error><Code>ServiceUnavailable</Code><Message>secret-fixture-token</Message></Error>")
		case "DELETE":
			if !putKeys[r.URL.Path] || !strings.Contains(r.URL.Path, ".connection-test-") {
				t.Error("cleanup targeted an object not created by this test")
			}
			deletes++
			delete(objects, r.URL.Path)
			w.WriteHeader(204)
		default:
			t.Error("GET must not follow a failed PUT")
			w.WriteHeader(405)
		}
	}))
	defer fixture.Close()
	m, _ := newTestManager(t)
	allowStorageFixture(t, m, fixture.URL)
	in := storageAuditInput(fixture.URL)
	saved, err := m.SaveStorage(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	result, err := m.TestStorageDetailed(ctx, in)
	if err == nil || result.Status != "failed" || result.Stage != "writing" || result.ErrorCode == "" {
		t.Fatalf("failed PUT reported incorrectly: %+v %v", result, err)
	}
	mu.Lock()
	if len(objects) != 0 || deletes != 1 || puts != 1 {
		t.Errorf("ambiguous write not cleaned or retried: remaining=%d deletes=%d puts=%d", len(objects), deletes, puts)
	}
	mu.Unlock()
	if result.CleanupErrorCode != "" || result.CleanupMessage == "" {
		t.Fatalf("cleanup outcome not reported: %+v", result)
	}
	stored, _, err := m.opts.Store.BackupStorage(context.Background(), saved.ID)
	if err != nil || stored.Health.Stage != result.Stage || stored.Health.ErrorCode != result.ErrorCode || stored.Health.CleanupMessage != result.CleanupMessage {
		t.Fatalf("detailed health missing: %+v %v", stored.Health, err)
	}
	public, _ := json.Marshal(struct {
		Result domain.BackupStorageTestResult
		Health domain.BackupStorageHealth
		Error  string
	}{result, stored.Health, errText(err)})
	for _, secret := range []string{"secret-fixture-token", in.AccessKeyID, in.SecretAccessKey} {
		if strings.Contains(string(public), secret) {
			t.Fatal("diagnostic contains credential or upstream response")
		}
	}
}

func TestStorageDetailedTestCanceledAfterWriteStillDeletesWithIndependentContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var mu sync.Mutex
	objects := map[string][]byte{}
	deletes := 0
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "PUT":
			body, _ := io.ReadAll(r.Body)
			mu.Lock()
			objects[r.URL.Path] = body
			mu.Unlock()
			cancel()
			w.WriteHeader(200)
		case "DELETE":
			mu.Lock()
			deletes++
			delete(objects, r.URL.Path)
			mu.Unlock()
			w.WriteHeader(204)
		default:
			w.WriteHeader(405)
		}
	}))
	defer fixture.Close()
	m, _ := newTestManager(t)
	allowStorageFixture(t, m, fixture.URL)
	result, err := m.TestStorageDetailed(ctx, storageAuditInput(fixture.URL))
	if err == nil || result.Status != "failed" || result.ErrorCode != "canceled" {
		t.Fatalf("cancellation result incorrect: %+v %v", result, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(objects) != 0 || deletes != 1 || result.CleanupErrorCode != "" {
		t.Fatalf("canceled write cleanup failed: remaining=%d deletes=%d result=%+v", len(objects), deletes, result)
	}
}

func TestStorageDetailedReadAndCleanupFailureRemainSeparate(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "PUT":
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(200)
		case "GET":
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(404)
			_, _ = io.WriteString(w, "<Error><Code>NoSuchKey</Code><Message>raw-secret-response</Message></Error>")
		case "DELETE":
			w.Header().Set("Content-Type", "application/xml")
			w.WriteHeader(403)
			_, _ = io.WriteString(w, "<Error><Code>AccessDenied</Code><Message>raw-secret-response</Message></Error>")
		}
	}))
	defer fixture.Close()
	m, _ := newTestManager(t)
	allowStorageFixture(t, m, fixture.URL)
	result, err := m.TestStorageDetailed(context.Background(), storageAuditInput(fixture.URL))
	if err == nil || result.Stage != "reading" || result.ErrorCode != "object_not_found" || result.HTTPStatus != 404 || result.CleanupErrorCode != "permission_denied" || !strings.Contains(result.CleanupMessage, "可能有测试对象残留") {
		t.Fatalf("cleanup overwrote primary failure or missing diagnosis: %+v %v", result, err)
	}
	encoded, _ := json.Marshal(result)
	if strings.Contains(string(encoded), "raw-secret-response") {
		t.Fatal("raw upstream error leaked")
	}
}

func TestStorageDetailedCleanupFailureCannotReportSuccess(t *testing.T) {
	var payloadMu sync.Mutex
	var payload []byte
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case "PUT":
			payloadMu.Lock()
			payload, _ = io.ReadAll(r.Body)
			payloadMu.Unlock()
			w.WriteHeader(200)
		case "GET":
			payloadMu.Lock()
			_, _ = w.Write(payload)
			payloadMu.Unlock()
		case "DELETE":
			w.WriteHeader(403)
		}
	}))
	defer fixture.Close()
	m, _ := newTestManager(t)
	allowStorageFixture(t, m, fixture.URL)
	result, err := m.TestStorageDetailed(context.Background(), storageAuditInput(fixture.URL))
	if err == nil || result.Status != "failed" || result.Stage != "cleaning" || result.CleanupErrorCode == "" {
		t.Fatalf("cleanup failure reported success: %+v %v", result, err)
	}
}

func TestStorageDetailedDoesNotDeletePreexistingConditionalConflict(t *testing.T) {
	var deletes int
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			deletes++
			w.WriteHeader(204)
			return
		}
		if r.Method != "PUT" || r.Header.Get("If-None-Match") != "*" {
			t.Error("expected conditional PUT")
		}
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(412)
		_, _ = io.WriteString(w, "<Error><Code>PreconditionFailed</Code><Message>preexisting-object</Message></Error>")
	}))
	defer fixture.Close()
	m, _ := newTestManager(t)
	allowStorageFixture(t, m, fixture.URL)
	result, err := m.TestStorageDetailed(context.Background(), storageAuditInput(fixture.URL))
	if err == nil || result.ErrorCode != "object_conflict" || deletes != 0 || result.CleanupErrorCode != "" {
		t.Fatalf("preexisting object was changed: %+v deletes=%d err=%v", result, deletes, err)
	}
}

func TestStorageFailureClassifiesOnlyControlledDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name, stage, code, resultStage string
		cause                          error
	}{
		{"dns", "writing", "dns_error", "dns", &net.DNSError{Err: "raw-credential", Name: "secret-host"}},
		{"fakeip", "writing", "fake_ip_blocked", "dns", &upstream.Error{Code: "fake_ip_blocked", Message: "raw-credential"}},
		{"blocked", "writing", "blocked_target", "dns", &upstream.Error{Code: "blocked_target", Message: "raw-credential"}},
		{"connect", "writing", "connection_error", "connecting", &upstream.Error{Code: "connection_error", Message: "raw-credential"}},
		{"timeout", "reading", "timeout", "reading", context.DeadlineExceeded},
		{"canceled", "writing", "canceled", "writing", context.Canceled},
		{"authentication", "writing", "authentication_failed", "writing", &smithy.GenericAPIError{Code: "SignatureDoesNotMatch", Message: "raw-credential"}},
		{"permission", "reading", "permission_denied", "reading", &smithy.GenericAPIError{Code: "AccessDenied", Message: "raw-credential"}},
		{"bucket", "writing", "bucket_not_found", "writing", &smithy.GenericAPIError{Code: "NoSuchBucket", Message: "raw-credential"}},
		{"generic", "cleaning", "cleanup_failed", "cleaning", errors.New("raw-credential")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			failure := storageFailure(context.Background(), tc.stage, tc.cause)
			if failure.Code != tc.code || failure.Stage != tc.resultStage || strings.Contains(failure.Error(), "raw-credential") || strings.Contains(failure.Error(), "secret-host") {
				t.Fatalf("unsafe or incorrect classification: %+v", failure)
			}
		})
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
