package backups

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"relaylab/internal/backup"
	"relaylab/internal/domain"
	"relaylab/internal/operationlock"
)

// This fixture executes the real archive pipeline and stops at the exact state
// a crash leaves after final publication but before the final run commit.
func publishBeforeFinalCommit(t *testing.T, m *Manager, policy Schedule, trusted bool) Run {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	id := domain.NewID()
	r := Run{ID: id, RequestID: id, State: "snapshotting", Trigger: "scheduled", CreatedAt: now, StartedAt: now, HeartbeatAt: now, Version: "1.5.0", DatabaseSchema: 4, RecipientID: policy.RecipientID, FileName: fmt.Sprintf("relaylab_%s_%s.zip.age", now.Format("20060102T150405Z"), id), LocalState: "pending", PolicySnapshot: &policy}
	r.LocalPath = filepath.Join(m.opts.BackupDir, r.FileName)
	if _, _, e := m.opts.Store.CreateBackupRun(ctx, r); e != nil {
		t.Fatal(e)
	}
	ready := 0
	opts := backup.Options{Recipient: policy.Recipient, Schema: 4, BackupID: r.ID, OnPhase: func(state string) error { r.State = state; return m.save(ctx, r) }}
	if trusted {
		opts.OnReady = func(member backup.Member) error {
			ready++
			if _, e := os.Stat(r.LocalPath); !os.IsNotExist(e) {
				return fmt.Errorf("final name visible before digest commit: %v", e)
			}
			r.SHA256, r.SizeBytes = member.SHA256, member.Size
			return m.save(ctx, r)
		}
	}
	if e := backup.CreateFromStore(ctx, m.opts.Store, m.opts.DataDir, r.LocalPath, opts); e != nil {
		t.Fatal(e)
	}
	if trusted && ready != 1 {
		t.Fatal("digest callback not called exactly once")
	}
	if _, e := os.Stat(r.LocalPath); e != nil {
		t.Fatal("archive never published", e)
	}
	return r
}

func TestArchiveRecoveryRequiresPrepublicationDigest(t *testing.T) {
	for _, variant := range []string{"ready", "missing-digest", "tampered"} {
		t.Run(variant, func(t *testing.T) {
			m, _ := newTestManager(t)
			ctx := context.Background()
			p, e := m.opts.Store.BackupPolicy(ctx)
			if e != nil {
				t.Fatal(e)
			}
			r := publishBeforeFinalCommit(t, m, p, variant != "missing-digest")
			if variant == "tampered" {
				contents, e := os.ReadFile(r.LocalPath)
				if e != nil {
					t.Fatal(e)
				}
				contents[len(contents)-1] ^= 1
				if e = os.WriteFile(r.LocalPath, contents, 0600); e != nil {
					t.Fatal(e)
				}
			}
			if !m.recoverInterrupted() {
				t.Fatal("recovery did not acquire lock")
			}
			actual, e := m.Record(ctx, r.ID)
			if e != nil {
				t.Fatal(e)
			}
			if variant == "ready" {
				if actual.State != "succeeded" || actual.LocalState != "verified" {
					t.Fatal(actual)
				}
			} else {
				if actual.State != "interrupted" || actual.LocalState != "unverified" || actual.ErrorCode != "local_verification_required" {
					t.Fatal("untrusted file was accepted", actual)
				}
				if actual.SHA256 != r.SHA256 || actual.SizeBytes != r.SizeBytes {
					t.Fatal("recovery invented a new trusted digest")
				}
			}
		})
	}
}

func TestPeriodicCleanupOnlyRemovesExpiredOwnedTemporaryFiles(t *testing.T) {
	m, _ := newTestManager(t)
	root := m.opts.BackupDir
	now := time.Now()
	old := now.Add(-3 * time.Hour)
	oldPartial := filepath.Join(root, ".backup-expired.partial")
	freshPartial := filepath.Join(root, ".backup-fresh.partial")
	committed := filepath.Join(root, "unregistered.zip.age")
	for _, p := range []string{oldPartial, freshPartial, committed} {
		if e := os.WriteFile(p, []byte("fixture"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.Chtimes(oldPartial, old, old); e != nil {
		t.Fatal(e)
	}
	if e := os.Chtimes(committed, old, old); e != nil {
		t.Fatal(e)
	}
	staging := filepath.Join(root, ".staging")
	stale := filepath.Join(staging, "archive-expired")
	fresh := filepath.Join(staging, "validate-fresh")
	unknown := filepath.Join(staging, "user-kept")
	for _, p := range []string{stale, fresh, unknown} {
		if e := os.MkdirAll(p, 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(filepath.Join(p, "master.key"), []byte("fixture"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	if e := os.Chtimes(stale, old, old); e != nil {
		t.Fatal(e)
	}
	if e := os.Chtimes(unknown, old, old); e != nil {
		t.Fatal(e)
	}
	download := filepath.Join(staging, "download-expired.zip.age")
	if e := os.WriteFile(download, []byte("fixture"), 0600); e != nil {
		t.Fatal(e)
	}
	if e := os.Chtimes(download, old, old); e != nil {
		t.Fatal(e)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if e := os.MkdirAll(outside, 0700); e != nil {
		t.Fatal(e)
	}
	outsideFile := filepath.Join(outside, "keep")
	if e := os.WriteFile(outsideFile, []byte("keep"), 0600); e != nil {
		t.Fatal(e)
	}
	link := filepath.Join(staging, "archive-link")
	linkCreated := os.Symlink(outside, link) == nil
	// Removing an expired owned folder must not traverse links inside it.
	if os.Symlink(outside, filepath.Join(stale, "outside-link")) == nil {
		if e := os.Chtimes(stale, old, old); e != nil {
			t.Fatal(e)
		}
	}
	release, e := operationlock.Acquire(m.opts.DataDir)
	if e != nil {
		t.Fatal(e)
	}
	if m.periodicCleanup(now) {
		t.Fatal("cleanup ran while another operation held the lock")
	}
	_ = release()
	if _, e = os.Stat(oldPartial); e != nil {
		t.Fatal("busy cleanup removed a file", e)
	}
	if !m.periodicCleanup(now) {
		t.Fatal("cleanup did not run")
	}
	for _, p := range []string{oldPartial, stale, download} {
		if _, e = os.Lstat(p); !os.IsNotExist(e) {
			t.Fatal("expired temporary file retained", p, e)
		}
	}
	for _, p := range []string{freshPartial, committed, fresh, unknown, outsideFile} {
		if _, e = os.Stat(p); e != nil {
			t.Fatal("cleanup removed a protected or unknown file", p, e)
		}
	}
	if linkCreated {
		if _, e = os.Lstat(link); e != nil {
			t.Fatal("cleanup touched a symlink", e)
		}
	}
}
