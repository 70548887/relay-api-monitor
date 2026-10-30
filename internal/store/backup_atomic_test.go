package store

import (
	"context"
	"path/filepath"
	"relaylab/internal/domain"
	"testing"
)

func TestBackupCompletionRollsBackArchiveClaimsWhenRunCommitFails(t *testing.T) {
	st, e := Open(filepath.Join(t.TempDir(), "db.sqlite"))
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	ctx := context.Background()
	_, e = st.CompleteBackupRun(ctx, domain.BackupRun{ID: "missing-run", State: "succeeded"}, []string{"2026-10-01"}, 0)
	if e == nil {
		t.Fatal("missing run completion succeeded")
	}
	var count int
	if e = st.DB.QueryRowContext(ctx, "SELECT COUNT(*) FROM backup_archive_slots").Scan(&count); e != nil || count != 0 {
		t.Fatalf("orphan archive ownership committed: %d %v", count, e)
	}
}
