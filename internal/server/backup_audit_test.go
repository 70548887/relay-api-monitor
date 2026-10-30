package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"relaylab/internal/backup"
	"relaylab/internal/domain"
	"relaylab/internal/launcher"
	"relaylab/internal/restorestate"
	"relaylab/internal/security"
)

type definiteRestoreRejection struct{}

func (definiteRestoreRejection) Error() string         { return "rejected" }
func (definiteRestoreRejection) RestoreRejected() bool { return true }

func preflightForHTTPAudit(t *testing.T, s *Server, dir string) string {
	t.Helper()
	input := filepath.Join(t.TempDir(), "restore.zip")
	if e := backup.CreateFromStore(context.Background(), s.Store, dir, input, backup.Options{Legacy: true}); e != nil {
		t.Fatal(e)
	}
	id := domain.NewID()
	v, e := backup.Validate(context.Background(), input, "", restorestate.Stage(dir, id))
	if e != nil {
		t.Fatal(e)
	}
	db, _, e := launcher.SHA256File(v.DBPath)
	if e != nil {
		t.Fatal(e)
	}
	key, _, e := launcher.SHA256File(v.KeyPath)
	if e != nil {
		t.Fatal(e)
	}
	if e = restorestate.SavePreflight(dir, restorestate.Preflight{ID: id, Dir: v.Dir, DBHash: db, KeyHash: key, ExpiresAt: time.Now().Add(30 * time.Minute)}); e != nil {
		t.Fatal(e)
	}
	return id
}

func TestBackupAuditReauthErrorDoesNotInvalidateSession(t *testing.T) {
	_, h, _, _ := backupTestApp(t)
	cookie, csrf := loginForTest(t, h)
	w := request(t, h, "POST", "/api/v1/admin/backups/reauth", map[string]string{"password": "wrong"}, cookie, csrf)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
	if w = request(t, h, "GET", "/api/v1/auth/me", nil, cookie, ""); w.Code != 200 {
		t.Fatal("valid session was lost", w.Code)
	}
}

func TestBackupAuditFilteredRecordsAndRetentionPreview(t *testing.T) {
	s, h, _, _ := backupTestApp(t)
	cookie, csrf := loginForTest(t, h)
	ctx := context.Background()
	base := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 25; i++ {
		state, trigger := "failed", "manual"
		monthly := false
		if i == 0 {
			state, trigger, monthly = "succeeded", "scheduled", true
		}
		run := domain.BackupRun{ID: fmt.Sprintf("fixture-%d", i), RequestID: fmt.Sprintf("request-%d", i), State: state, Trigger: trigger, MonthlyArchive: monthly, ArchiveRetainCount: 0, CreatedAt: base.Add(time.Duration(i) * time.Second), StartedAt: base.Add(time.Duration(i) * time.Second)}
		if _, _, e := s.Store.CreateBackupRun(ctx, run); e != nil {
			t.Fatal(e)
		}
	}
	w := request(t, h, "GET", "/api/v1/admin/backups/records?state=succeeded&trigger=scheduled&protection=monthly&limit=1", nil, cookie, "")
	var rows []domain.BackupRun
	if e := json.Unmarshal(w.Body.Bytes(), &rows); w.Code != 200 || e != nil || len(rows) != 1 || rows[0].ID != "fixture-0" {
		t.Fatal(w.Code, w.Body.String())
	}
	w = request(t, h, "GET", "/api/v1/admin/backups/records?from=invalid", nil, cookie, "")
	if w.Code != 400 {
		t.Fatal("invalid time accepted", w.Code)
	}
	for i := 0; i < 4; i++ {
		run := domain.BackupRun{ID: fmt.Sprintf("valid-%d", i), RequestID: fmt.Sprintf("valid-request-%d", i), State: "succeeded", Trigger: "manual", LocalState: "verified", SizeBytes: 1024, CreatedAt: base.Add(time.Duration(i+100) * time.Second), StartedAt: base.Add(time.Duration(i+100) * time.Second)}
		if _, _, e := s.Store.CreateBackupRun(ctx, run); e != nil {
			t.Fatal(e)
		}
	}
	policy := domain.DefaultBackupSchedule()
	policy.RetainCount = 1
	policy.RetainDays = 0
	w = request(t, h, "POST", "/api/v1/admin/backups/schedule/retention-preview", policy, cookie, csrf)
	var preview struct {
		Count int   `json:"count"`
		Local int   `json:"local_copies"`
		Bytes int64 `json:"total_bytes"`
	}
	if e := json.Unmarshal(w.Body.Bytes(), &preview); w.Code != 200 || e != nil || preview.Count != 3 || preview.Local != 3 || preview.Bytes != 3072 {
		t.Fatal(w.Code, w.Body.String())
	}
	stored, e := s.Store.BackupPolicy(ctx)
	if e != nil || stored.RetainCount != 10 {
		t.Fatal("preview changed policy", stored, e)
	}
}

func TestBackupAuditRestoreSubmissionCannotOverwriteLauncherJournal(t *testing.T) {
	for _, definite := range []bool{false, true} {
		t.Run(fmt.Sprint(definite), func(t *testing.T) {
			s, h, _, dir := backupTestApp(t)
			cookie, csrf := loginForTest(t, h)
			p := preflightForHTTPAudit(t, s, dir)
			var calls atomic.Int32
			s.ConfigureBackupRestore(true, func(_ context.Context, r launcher.RestoreRequest) error {
				calls.Add(1)
				if definite {
					return definiteRestoreRejection{}
				}
				var op restorestate.Operation
				if e := restorestate.ReadOperation(dir, r.OperationID, &op); e != nil {
					return e
				}
				op.State = "replacing"
				op.DataChanged = true
				op.SafetyDir = "must-survive"
				if e := restorestate.SaveOperation(dir, op); e != nil {
					return e
				}
				return errors.New("response lost")
			})
			if w := request(t, h, "POST", "/api/v1/admin/backups/reauth", map[string]string{"password": "password-test-123456"}, cookie, csrf); w.Code != 200 {
				t.Fatal(w.Body.String())
			}
			w := request(t, h, "POST", "/api/v1/admin/backups/restore/apply", map[string]string{"preflight_id": p}, cookie, csrf)
			var result struct {
				ID string `json:"operation_id"`
			}
			_ = json.Unmarshal(w.Body.Bytes(), &result)
			if w.Code != 202 {
				t.Fatal(w.Body.String())
			}
			var op restorestate.Operation
			until := time.Now().Add(5 * time.Second)
			for time.Now().Before(until) {
				_ = restorestate.ReadOperation(dir, result.ID, &op)
				if definite && op.State == "failed" || !definite && op.State == "replacing" {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if definite {
				if op.State != "failed" || op.ErrorCode != "launcher_rejected" {
					t.Fatal(op)
				}
			} else {
				if op.State != "replacing" || !op.DataChanged || op.SafetyDir != "must-survive" {
					t.Fatal("journal was overwritten", op)
				}
			}
			w = request(t, h, "POST", "/api/v1/admin/backups/restore/apply", map[string]string{"preflight_id": p}, cookie, csrf)
			if w.Code != 409 || calls.Load() != 1 {
				t.Fatal("preflight replayed", w.Code, calls.Load())
			}
		})
	}
}

func TestBackupAuditForeignOperationRejected(t *testing.T) {
	_, h, _, dir := backupTestApp(t)
	cookie, _ := loginForTest(t, h)
	id := domain.NewID()
	op := restorestate.Operation{ID: id, DataDir: "another-instance", TokenHash: security.HashToken("fixture-token"), TokenExpiresAt: time.Now().Add(time.Hour)}
	if e := os.MkdirAll(filepath.Dir(restorestate.OperationPath(dir, id)), 0700); e != nil {
		t.Fatal(e)
	}
	if e := restorestate.Save(restorestate.OperationPath(dir, id), op); e != nil {
		t.Fatal(e)
	}
	w := request(t, h, http.MethodGet, "/api/v1/admin/backups/operations/"+id, nil, cookie, "")
	if w.Code != 404 {
		t.Fatal("foreign operation exposed", w.Code)
	}
}

func TestBackupAuditHeavyWorkIsBoundedAndDownloadsObserveMaintenance(t *testing.T) {
	s, h, _, _ := backupTestApp(t)
	cookie, csrf := loginForTest(t, h)
	s.backupHTTP.ioSlots <- struct{}{}
	s.backupHTTP.ioSlots <- struct{}{}
	w := request(t, h, "POST", "/api/v1/admin/backups/storage/test", map[string]string{}, cookie, csrf)
	if w.Code != 429 {
		t.Fatal("heavy work was not bounded", w.Code)
	}
	<-s.backupHTTP.ioSlots
	<-s.backupHTTP.ioSlots
	w = request(t, h, "POST", "/api/v1/admin/backups/reauth", map[string]string{"password": "password-test-123456"}, cookie, csrf)
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	s.SetMaintenance(true)
	w = request(t, h, "GET", "/api/v1/admin/backups/records/unknown/download", nil, cookie, "")
	if w.Code != 503 {
		t.Fatal("new download admitted during maintenance", w.Code)
	}
}

func TestBackupAuditExpiredUnacceptedSubmissionCanBeCleaned(t *testing.T) {
	for _, registered := range []bool{false, true} {
		t.Run(fmt.Sprint(registered), func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "data")
			if e := os.MkdirAll(dir, 0700); e != nil {
				t.Fatal(e)
			}
			id, pid := domain.NewID(), domain.NewID()
			now := time.Now().UTC()
			p := restorestate.Preflight{ID: pid, ExpiresAt: now.Add(-time.Hour)}
			if e := restorestate.SavePreflight(dir, p); e != nil {
				t.Fatal(e)
			}
			op := restorestate.Operation{ID: id, PreflightID: pid, State: "accepted", StartedAt: now.Add(-2 * time.Hour)}
			if e := restorestate.SaveOperation(dir, op); e != nil {
				t.Fatal(e)
			}
			if registered {
				if e := restorestate.SaveCurrent(dir, id); e != nil {
					t.Fatal(e)
				}
			}
			cleanupOrphanRestoreSubmissions(dir, now)
			if e := restorestate.ReadOperation(dir, id, &op); e != nil {
				t.Fatal(e)
			}
			if registered && op.State != "accepted" {
				t.Fatal("registered transaction changed", op)
			}
			if !registered && op.State != "failed" {
				t.Fatal("orphan not terminated", op)
			}
			restorestate.Cleanup(dir, now)
			_, e := os.Stat(restorestate.Stage(dir, pid))
			if registered && e != nil {
				t.Fatal("active stage deleted", e)
			}
			if !registered && !os.IsNotExist(e) {
				t.Fatal("expired plaintext stage left behind", e)
			}
		})
	}
}
