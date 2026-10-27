package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"relaylab/internal/domain"
)

func TestPrivateRunRecoveryRetentionAndConnectionDeletion(t *testing.T) {
	ctx := context.Background()
	s, e := Open(filepath.Join(t.TempDir(), "private.db"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	user, e := s.CreateMember(ctx, "member", "fixture-hash")
	if e != nil {
		t.Fatal(e)
	}
	now := time.Now().UTC()
	connection := domain.UserConnection{ID: "connection", UserID: user.ID, Name: "saved", EncryptedKey: "cipher", CredentialFingerprint: "fp", Revision: 1, CreatedAt: now, UpdatedAt: now}
	if e = s.SaveUserConnection(ctx, connection); e != nil {
		t.Fatal(e)
	}
	run := domain.OnlineTestRun{ID: "active", UserID: user.ID, ConnectionID: connection.ID, ConnectionRevision: connection.Revision, ClientRequestID: "one", RequestHash: "hash", State: "running", ProcessID: "old", CreatedAt: now, StartedAt: now, Text: "partial", Prompt: "prompt"}
	if _, created, e := s.CreateOnlineTestRun(ctx, run); e != nil || !created {
		t.Fatal(e)
	}
	if e = s.DeleteOnlineTestRuns(ctx, user.ID, []string{run.ID}); !errors.Is(e, ErrConflict) {
		t.Fatal("active record deleted", e)
	}
	if e = s.RecoverOnlineTestRuns(ctx, "new", now); e != nil {
		t.Fatal(e)
	}
	saved, e := s.GetOnlineTestRun(ctx, user.ID, run.ID)
	if e != nil || saved.State != "interrupted" || saved.Text != "partial" {
		t.Fatal(saved, e)
	}
	if e = s.DeleteUserConnection(ctx, user.ID, connection.ID); e != nil {
		t.Fatal(e)
	}
	saved, e = s.GetOnlineTestRun(ctx, user.ID, run.ID)
	if e != nil || saved.ConnectionID != "" || saved.Prompt != "prompt" {
		t.Fatal(saved, e)
	}
	// Retention deletes old terminal records, not credentials or current work.
	connection.ID = "persisted"
	connection.CredentialFingerprint = "new-fp"
	if e = s.SaveUserConnection(ctx, connection); e != nil {
		t.Fatal(e)
	}
	old := run
	old.ID = "old"
	old.ConnectionID = connection.ID
	old.ClientRequestID = "old"
	old.CreatedAt = now.AddDate(0, 0, -31)
	old.State = "created"
	old.ProcessID = "new"
	if _, _, e = s.CreateOnlineTestRun(ctx, old); e != nil {
		t.Fatal(e)
	}
	old.State = "succeeded"
	old.FinishedAt = &now
	if e = s.UpdateOnlineTestRun(ctx, old); e != nil {
		t.Fatal(e)
	}
	if e = s.CleanupOnlineTestRuns(ctx, now); e != nil {
		t.Fatal(e)
	}
	if _, e = s.GetOnlineTestRun(ctx, user.ID, old.ID); e == nil {
		t.Fatal("expired record remains")
	}
	if _, e = s.GetUserConnection(ctx, user.ID, connection.ID); e != nil {
		t.Fatal("cleanup removed connection")
	}
}
