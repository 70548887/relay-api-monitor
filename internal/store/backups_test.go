package store

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"relaylab/internal/domain"
	"relaylab/internal/security"
)

func TestBackupStorageEncryptionBindingAndSchema(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, e := Open(filepath.Join(dir, "relaylab.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	vault, e := security.LoadVault(dir, false)
	if e != nil {
		t.Fatal(e)
	}
	cipher, e := vault.EncryptBackupCredentials("storage-one", "access+secret")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = vault.DecryptBackupCredentials("storage-two", cipher); e == nil {
		t.Fatal("unbound storage credentials")
	}
	v := domain.BackupStorage{ID: "storage-one", Revision: 1}
	if e = s.SaveBackupStorage(ctx, v, cipher); e != nil {
		t.Fatal(e)
	}
	if e = s.ValidateBackupStorageKeys(ctx, vault); e != nil {
		t.Fatal(e)
	}
	var schema int
	s.DB.QueryRow("PRAGMA user_version").Scan(&schema)
	if schema != 4 {
		t.Fatal(schema)
	}
}
func TestPrepareRestoredDataTerminatesQueuedWorkAndFreezesBackup(t *testing.T) {
	s, e := Open(filepath.Join(t.TempDir(), "relaylab.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	if e = s.SavePlatform(ctx, domain.Platform{ID: "p", Name: "p"}); e != nil {
		t.Fatal(e)
	}
	// Controlled SQL fixtures isolate recovery from unrelated schedule defaults.
	for _, stmt := range []string{
		`INSERT INTO channels(id,platform_id,data) VALUES('c','p','{"id":"c"}')`,
		`INSERT INTO models(id,channel_id,upstream_id,data) VALUES('m','c','m','{"id":"m"}')`,
		`INSERT INTO schedules(id,model_id,kind,interval_seconds,next_run_at,enabled,data) VALUES('s','m','artifact',1800,'2020-01-01T00:00:00Z',1,'{"id":"s","next_run_at":"2020-01-01T00:00:00Z"}')`,
	} {
		if _, e = s.DB.Exec(stmt); e != nil {
			t.Fatal(e)
		}
	}
	for _, state := range []string{"queued", "claimed", "dispatching"} {
		b, _ := json.Marshal(domain.Job{ID: state, ModelID: "m", ChannelID: "c", Kind: "artifact", State: state})
		if _, e = s.DB.Exec(`INSERT INTO jobs(id,model_id,channel_id,kind,state,scheduled_at,created_at,data) VALUES(?,?,? ,?,?,?, ?,?)`, state, "m", "c", state, state, ts(now), ts(now), string(b)); e != nil {
			t.Fatal(e)
		}
	}
	p := domain.DefaultBackupSchedule()
	p.Enabled = true
	if e = s.SaveBackupPolicy(ctx, p); e != nil {
		t.Fatal(e)
	}
	if e = s.PrepareRestoredData(ctx, now); e != nil {
		t.Fatal(e)
	}
	for _, id := range []string{"queued", "claimed", "dispatching"} {
		var state, raw string
		if e = s.DB.QueryRow("SELECT state,data FROM jobs WHERE id=?", id).Scan(&state, &raw); e != nil {
			t.Fatal(e)
		}
		want := "canceled"
		if id == "dispatching" {
			want = "uncertain"
		}
		var j domain.Job
		if e = json.Unmarshal([]byte(raw), &j); e != nil {
			t.Fatal(e)
		}
		if state != want || j.State != want || j.FinishedAt == nil {
			t.Fatalf("restored %s %s %+v", id, state, j)
		}
	}
	p, e = s.BackupPolicy(ctx)
	if e != nil || p.Enabled || !p.ReconciliationRequired {
		t.Fatalf("unfenced %+v %v", p, e)
	}
	var next string
	if e = s.DB.QueryRow("SELECT next_run_at FROM schedules WHERE id='s'").Scan(&next); e != nil {
		t.Fatal(e)
	}
	parsed, e := time.Parse(time.RFC3339Nano, next)
	if e != nil || !parsed.Equal(now.Add(1800*time.Second)) {
		t.Fatalf("future schedule %s %v", next, e)
	}
}
